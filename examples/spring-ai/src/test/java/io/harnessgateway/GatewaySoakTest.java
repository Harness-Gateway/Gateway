package io.harnessgateway;

import java.lang.management.ManagementFactory;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.stream.Stream;

import com.openai.errors.OpenAIServiceException;
import com.openai.errors.RateLimitException;
import com.openai.errors.SseException;
import org.junit.jupiter.api.Timeout;
import org.junit.jupiter.api.condition.EnabledIfSystemProperty;
import org.junit.jupiter.api.condition.EnabledOnOs;
import org.junit.jupiter.api.condition.OS;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.boot.json.JsonParserFactory;
import reactor.core.Disposable;

import static org.junit.jupiter.api.Assertions.*;

/** Opt-in sustained Spring AI load; runtime metrics come from a test-only gateway. */
@Timeout(value = 2, unit = TimeUnit.HOURS)
@EnabledOnOs(OS.LINUX)
@EnabledIfSystemProperty(named = "gateway.soak.seconds", matches = "[1-9][0-9]*")
class GatewaySoakTest {
    private static final long MIB = 1024 * 1024;
    static Stream<String> models() { return Stream.of("codex", "copilot"); }

    private List<Path> processes(FixtureGateway server) throws Exception {
        try (var files = Files.list(server.pidsDirectory())) { return files.toList(); }
    }

    private void awaitIdle(FixtureGateway server) throws Exception {
        var deadline = System.nanoTime() + Duration.ofSeconds(3).toNanos();
        do {
            var alive = processes(server).stream().map(path -> Long.parseLong(path.getFileName().toString()))
                .filter(pid -> ProcessHandle.of(pid).map(ProcessHandle::isAlive).orElse(false)).toList();
            if (alive.isEmpty()) return;
            Thread.sleep(10);
        } while (System.nanoTime() < deadline);
        fail("Soak left live CLI processes: " + processes(server));
    }

    private Disposable hold(ChatClient client, CountDownLatch ready, List<Throwable> errors) {
        return client.prompt().user("hold").stream().content().subscribe(text -> {
            if ("ready".equals(text)) ready.countDown();
        }, errors::add);
    }

    private int round(FixtureGateway server, ChatClient client, HttpClient http) throws Exception {
        assertEquals("hello world", client.prompt().user("normal").call().content());
        assertEquals("hello world", client.prompt().user("normal").stream().content()
            .reduce("", String::concat).block(Duration.ofSeconds(10)));
        assertEquals(502, assertThrows(OpenAIServiceException.class,
            () -> client.prompt().user("crash").call().content()).statusCode());
        var partial = new CopyOnWriteArrayList<String>();
        var crash = assertThrows(SseException.class, () -> client.prompt().user("crash").stream().content()
            .doOnNext(partial::add).then().block(Duration.ofSeconds(10)));
        assertEquals("harness_error", crash.code().orElseThrow());
        assertTrue(partial.contains("ready"));
        assertEquals(504, assertThrows(OpenAIServiceException.class,
            () -> client.prompt().user("silent").call().content()).statusCode());
        var beforeCatalog = processes(server).size();
        var catalog = http.send(HttpRequest.newBuilder(URI.create(server.url() + "/models"))
            .timeout(Duration.ofSeconds(10)).build(), HttpResponse.BodyHandlers.ofString());
        assertEquals(200, catalog.statusCode());
        assertEquals(3, ((List<?>) JsonParserFactory.getJsonParser().parseMap(catalog.body()).get("data")).size());
        int catalogStarts = processes(server).size() - beforeCatalog;
        assertTrue(catalogStarts == 0 || catalogStarts == 1, "Discovery started duplicate CLI operations");
        assertEquals(List.of("ready"), client.prompt().user("hold").stream().content()
            .filter(text -> !text.isEmpty()).take(1).collectList().block(Duration.ofSeconds(10)));
        awaitIdle(server);
        var ready = new CountDownLatch(2);
        var errors = new CopyOnWriteArrayList<Throwable>();
        var holds = new ArrayList<Disposable>();
        var before = processes(server).size();
        try {
            holds.add(hold(client, ready, errors));
            holds.add(hold(client, ready, errors));
            assertTrue(ready.await(2, TimeUnit.SECONDS), "Admitted soak streams did not start: " + errors);
            try (var executor = Executors.newVirtualThreadPerTaskExecutor()) {
                var start = new CountDownLatch(1);
                var futures = java.util.stream.IntStream.range(0, 8).mapToObj(index -> executor.submit(() -> {
                    start.await();
                    return assertThrows(RateLimitException.class, () -> client.prompt().user("normal").call().content());
                })).toList();
                start.countDown();
                for (var future : futures) assertEquals(429, future.get(2, TimeUnit.SECONDS).statusCode());
            }
            assertEquals(before + 2, processes(server).size(), "Overload spawned an extra CLI");
            assertTrue(errors.isEmpty(), "Admitted streams failed during overload: " + errors);
        }
        finally { holds.forEach(Disposable::dispose); }
        awaitIdle(server);
        assertEquals("hello world", client.prompt().user("normal").call().content());
        awaitIdle(server);
        assertEquals(9 + catalogStarts, processes(server).size(), "Unexpected number of admitted CLI operations");
        // Keep process accounting bounded and avoid PID-reuse false positives
        // during long runs. A marker is removed only after its CLI was reaped.
        for (var path : processes(server)) Files.delete(path);
        return catalogStarts;
    }

    private record Sample(long rss, long heap, long objects, long goroutines, long descriptors) {}

    private Sample sample(FixtureGateway server) throws Exception {
        var runtime = server.sampleResources();
        var status = Files.readAllLines(Path.of("/proc", Long.toString(server.pid()), "status"));
        var rss = status.stream().filter(line -> line.startsWith("VmRSS:"))
            .mapToLong(line -> Long.parseLong(line.trim().split("\\s+")[1]) * 1024).findFirst().orElseThrow();
        long descriptors;
        try (var files = Files.list(Path.of("/proc", Long.toString(server.pid()), "fd"))) { descriptors = files.count(); }
        return new Sample(rss, ((Number) runtime.get("heap_alloc_bytes")).longValue(),
            ((Number) runtime.get("heap_objects")).longValue(), ((Number) runtime.get("goroutines")).longValue(), descriptors);
    }

    private void assertBounded(Sample baseline, Sample current) {
        assertTrue(current.rss <= baseline.rss + 128 * MIB, "RSS exceeded allowance: " + baseline + " -> " + current);
        assertTrue(current.heap <= baseline.heap + 32 * MIB, "Retained heap exceeded allowance: " + baseline + " -> " + current);
        assertTrue(current.objects <= baseline.objects + 50_000, "Heap objects accumulated: " + baseline + " -> " + current);
        assertTrue(current.goroutines <= baseline.goroutines + 16, "Goroutines accumulated: " + baseline + " -> " + current);
        assertTrue(current.descriptors <= baseline.descriptors + 12, "Descriptors accumulated: " + baseline + " -> " + current);
    }

    private long minimum(List<Sample> samples, java.util.function.ToLongFunction<Sample> value) {
        return samples.stream().mapToLong(value).min().orElseThrow();
    }

    @ParameterizedTest
    @MethodSource("models")
    void sustainedMixedLoadRemainsBounded(String model) throws Exception {
        long seconds = Long.parseLong(System.getProperty("gateway.soak.seconds"));
        try (var server = FixtureGateway.measured(model);
             var context = GatewayContractTest.application(model, server.url());
             var http = HttpClient.newHttpClient();
             var csv = Files.newBufferedWriter(server.directory().resolve("soak.csv"))) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            long refreshes = 0;
            long cacheHits = 0;
            for (int warmup = 0; warmup < 2; warmup++) {
                int starts = round(server, client, http);
                refreshes += starts;
                cacheHits += 1 - starts;
            }
            var baseline = sample(server);
            var samples = new ArrayList<Sample>();
            var started = System.nanoTime();
            long requests = 36;
            csv.write("elapsed_seconds,requests,rss_bytes,post_gc_heap_bytes,heap_objects,goroutines,descriptors,live_clis,jvm_heap_bytes,catalog_refreshes,catalog_cache_hits\n");
            do {
                int starts = round(server, client, http);
                refreshes += starts;
                cacheHits += 1 - starts;
                requests += 18;
                var current = sample(server);
                samples.add(current);
                csv.write(String.format(java.util.Locale.ROOT, "%.3f,%d,%d,%d,%d,%d,%d,0,%d,%d,%d%n",
                    (System.nanoTime() - started) / 1_000_000_000.0, requests, current.rss, current.heap,
                    current.objects, current.goroutines, current.descriptors,
                    ManagementFactory.getMemoryMXBean().getHeapMemoryUsage().getUsed(), refreshes, cacheHits));
                csv.flush();
                assertBounded(baseline, current);
            } while (System.nanoTime() - started < Duration.ofSeconds(seconds).toNanos());
            assertTrue(cacheHits > 0, "Repeated discovery never used the cache");
            if (seconds >= 60) assertTrue(refreshes >= 2, "Catalog never refreshed after expiry");
            if (samples.size() >= 6) {
                var early = samples.subList(0, 3);
                var late = samples.subList(samples.size() - 3, samples.size());
                assertTrue(minimum(late, Sample::heap) <= minimum(early, Sample::heap) + 8 * MIB, "Retained heap did not plateau: " + samples);
                assertTrue(minimum(late, Sample::rss) <= minimum(early, Sample::rss) + 32 * MIB, "RSS did not plateau: " + samples);
                assertTrue(minimum(late, Sample::goroutines) <= minimum(early, Sample::goroutines) + 8, "Goroutines did not plateau: " + samples);
                assertTrue(minimum(late, Sample::descriptors) <= minimum(early, Sample::descriptors) + 2, "Descriptors did not plateau: " + samples);
            }
            assertEquals("hello world", client.prompt().user("normal").call().content());
            awaitIdle(server);
            server.terminate();
            server.awaitExit();
            System.out.printf("Fixture soak %s: duration=%ds requests=%d samples=%d catalog_refreshes=%d catalog_cache_hits=%d baseline=%s final=%s CSV=%s%n",
                model, seconds, requests + 1, samples.size(), refreshes, cacheHits, baseline, samples.getLast(), server.directory().resolve("soak.csv"));
        }
    }
}
