package io.harnessgateway;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.concurrent.TimeUnit;
import java.util.regex.Pattern;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import org.springframework.boot.json.JsonParserFactory;

/** A real, disposable gateway process using Go CLI fixtures. */
final class FixtureGateway implements AutoCloseable {
    private final Process process;
    private final Path log;
    private final Path turnStarted;
    private final Path catalogStarted;
    private final Path catalogRelease;
    private final String url;
    private final Path directory;
    private final Path pids;
    private final Path metrics;
    private long sampleSequence;

    FixtureGateway(String model, String mode) throws Exception {
        this(model, mode, 1);
    }

    FixtureGateway(String model, String mode, int concurrency) throws Exception {
        this(model, mode, concurrency, "text");
    }

    FixtureGateway(String model, String mode, int concurrency, String logFormat) throws Exception {
        this(model, mode, concurrency, logFormat, false);
    }

    FixtureGateway(String model, String mode, int concurrency, String logFormat, String... flags) throws Exception {
        this(model, mode, concurrency, logFormat, false, flags);
    }

    static FixtureGateway measured(String model) throws Exception {
        return new FixtureGateway(model, "acceptance", 2, "text", true, "--timeout", "3s");
    }

    private FixtureGateway(String model, String mode, int concurrency, String logFormat, boolean measured, String... flags) throws Exception {
        directory = Files.createTempDirectory(Path.of(System.getProperty("gateway.failure.directory")), "gateway-");
        log = directory.resolve("gateway.log");
        metrics = directory.resolve("resources.json");
        pids = measured ? Files.createDirectory(directory.resolve("pids"))
            : Path.of(System.getProperty("gateway.failure.pids"));
        turnStarted = directory.resolve("turn-started");
        catalogStarted = directory.resolve("catalog-started");
        catalogRelease = directory.resolve("catalog-release");
        var args = new ArrayList<>(List.of(System.getProperty(measured ? "gateway.soak.executable" : "gateway.failure.executable"),
            "--listen", "127.0.0.1:0", "--harnesses", model,
            "--" + model + "-path", System.getProperty("gateway.failure." + model),
            "--timeout", "30s", "--concurrency", Integer.toString(concurrency), "--log-format", logFormat));
        args.addAll(List.of(flags));
        var builder = new ProcessBuilder(args);
        var env = builder.environment();
        env.put("HARNESS_GATEWAY_" + model.toUpperCase(java.util.Locale.ROOT) + "_TEST", mode);
        env.put("HARNESS_GATEWAY_ACCEPTANCE_PIDS", pids.toString());
        if (measured) env.put("HARNESS_GATEWAY_SOAK_METRICS", metrics.toString());
        env.put("HARNESS_GATEWAY_ACCEPTANCE_STARTUP", directory.resolve("startup-once").toString());
        env.put("HARNESS_GATEWAY_ACCEPTANCE_TURN", turnStarted.toString());
        env.put("HARNESS_GATEWAY_ACCEPTANCE_CATALOG", catalogStarted.toString());
        env.put("HARNESS_GATEWAY_ACCEPTANCE_CATALOG_RELEASE", catalogRelease.toString());
        env.put("HARNESS_GATEWAY_COPILOT_LOG", directory.resolve("copilot-methods").toString());
        env.put("HARNESS_GATEWAY_COPILOT_PID", directory.resolve("copilot-pid").toString());
        process = builder.redirectErrorStream(true).redirectOutput(log.toFile()).start();
        try {
            url = awaitReady();
        }
        catch (Exception | AssertionError error) {
            process.destroyForcibly().waitFor();
            throw error;
        }
    }

    String url() { return url + "/v1"; }

    long pid() { return process.pid(); }

    Path directory() { return directory; }

    Path pidsDirectory() { return pids; }

    long startedCliCount() throws Exception {
        try (var files = Files.list(pids)) { return files.count(); }
    }

    Map<String, Object> sampleResources() throws Exception {
        var request = Long.toString(++sampleSequence);
        Files.writeString(Path.of(metrics + ".request"), request);
        var parser = JsonParserFactory.getJsonParser();
        var deadline = System.nanoTime() + Duration.ofSeconds(5).toNanos();
        while (System.nanoTime() < deadline && process.isAlive()) {
            if (Files.exists(metrics)) {
                var snapshot = parser.parseMap(Files.readString(metrics));
                if (request.equals(snapshot.get("request"))) return snapshot;
            }
            Thread.sleep(20);
        }
        throw new AssertionError("Gateway resource sample did not arrive: " + logs());
    }

    String logs() throws Exception { return Files.readString(log); }

    void terminate() { process.destroy(); } // SIGTERM on the supported Unix hosts.

    void awaitTurnStarted() throws Exception {
        awaitMarker(turnStarted, "the inference request");
    }

    void awaitModelDiscoveryStarted() throws Exception {
        awaitMarker(catalogStarted, "model discovery");
    }

    private void awaitMarker(Path marker, String operation) throws Exception {
        var deadline = System.nanoTime() + Duration.ofSeconds(5).toNanos();
        while (System.nanoTime() < deadline && process.isAlive()) {
            if (Files.exists(marker)) {
                return;
            }
            Thread.sleep(20);
        }
        throw new AssertionError("CLI did not accept " + operation + ": " + Files.readString(log));
    }

    void awaitExit() throws Exception {
        if (!process.waitFor(6, TimeUnit.SECONDS)) {
            throw new AssertionError("Gateway did not exit: " + Files.readString(log));
        }
        if (process.exitValue() != 0) {
            throw new AssertionError("Gateway exited with " + process.exitValue() + ": " + Files.readString(log));
        }
    }

    void releaseModelDiscovery() throws Exception {
        Files.writeString(catalogRelease, "release");
    }

    void failModelDiscovery() throws Exception {
        Files.writeString(catalogRelease, "error");
    }

    private String awaitReady() throws Exception {
        var pattern = Pattern.compile("(?:url=|\"url\":\")(http://127\\.0\\.0\\.1:[0-9]+)");
        var deadline = System.nanoTime() + Duration.ofSeconds(5).toNanos();
        try (var client = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(1)).build()) {
            while (System.nanoTime() < deadline && process.isAlive()) {
                var matcher = pattern.matcher(Files.readString(log));
                if (matcher.find()) {
                    var address = matcher.group(1);
                    var request = HttpRequest.newBuilder(URI.create(address + "/health"))
                        .timeout(Duration.ofSeconds(1)).build();
                    if (client.send(request, HttpResponse.BodyHandlers.discarding()).statusCode() == 200) {
                        return address;
                    }
                }
                Thread.sleep(20);
            }
        }
        throw new AssertionError("Gateway did not become ready: " + Files.readString(log));
    }

    @Override
    public void close() throws Exception {
        if (process.isAlive()) {
            process.destroy();
            if (!process.waitFor(6, TimeUnit.SECONDS)) {
                process.destroyForcibly().waitFor();
                throw new AssertionError("Gateway needed forced cleanup: " + Files.readString(log));
            }
        }
    }
}
