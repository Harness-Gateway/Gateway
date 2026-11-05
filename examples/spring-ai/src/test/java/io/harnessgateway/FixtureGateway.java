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

/** A real, disposable gateway process using Go CLI fixtures. */
final class FixtureGateway implements AutoCloseable {
    private final Process process;
    private final Path log;
    private final Path turnStarted;
    private final String url;

    FixtureGateway(String model, String mode) throws Exception {
        this(model, mode, 1);
    }

    FixtureGateway(String model, String mode, int concurrency) throws Exception {
        var directory = Files.createTempDirectory(Path.of(System.getProperty("gateway.failure.directory")), "gateway-");
        log = directory.resolve("gateway.log");
        turnStarted = directory.resolve("turn-started");
        var builder = new ProcessBuilder(System.getProperty("gateway.failure.executable"),
            "-listen", "127.0.0.1:0", "-harnesses", model,
            "-" + model + "-path", System.getProperty("gateway.failure." + model),
            "-timeout", "30s", "-concurrency", Integer.toString(concurrency));
        var env = builder.environment();
        env.put("HARNESS_GATEWAY_" + model.toUpperCase(java.util.Locale.ROOT) + "_TEST", mode);
        env.put("HARNESS_GATEWAY_ACCEPTANCE_PIDS", System.getProperty("gateway.failure.pids"));
        env.put("HARNESS_GATEWAY_ACCEPTANCE_STARTUP", directory.resolve("startup-once").toString());
        env.put("HARNESS_GATEWAY_ACCEPTANCE_TURN", turnStarted.toString());
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

    void terminate() { process.destroy(); } // SIGTERM on the supported Unix hosts.

    void awaitTurnStarted() throws Exception {
        var deadline = System.nanoTime() + Duration.ofSeconds(5).toNanos();
        while (System.nanoTime() < deadline && process.isAlive()) {
            if (Files.exists(turnStarted)) {
                return;
            }
            Thread.sleep(20);
        }
        throw new AssertionError("CLI did not accept the inference request: " + Files.readString(log));
    }

    void awaitExit() throws Exception {
        if (!process.waitFor(6, TimeUnit.SECONDS)) {
            throw new AssertionError("Gateway did not exit: " + Files.readString(log));
        }
        if (process.exitValue() != 0) {
            throw new AssertionError("Gateway exited with " + process.exitValue() + ": " + Files.readString(log));
        }
    }

    private String awaitReady() throws Exception {
        var pattern = Pattern.compile("url=(http://127\\.0\\.0\\.1:[0-9]+)");
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
