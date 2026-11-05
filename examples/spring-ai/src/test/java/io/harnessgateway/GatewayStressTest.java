package io.harnessgateway;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.stream.IntStream;
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
import reactor.core.Disposable;

import org.springframework.ai.chat.client.ChatClient;
import org.springframework.boot.WebApplicationType;
import org.springframework.boot.builder.SpringApplicationBuilder;

import static org.junit.jupiter.api.Assertions.*;

/** Repeated real adapter/process lifecycle checks without provider inference. */
@Timeout(90)
@EnabledOnOs(OS.LINUX) // Descriptor accounting uses /proc; CI also runs on Linux.
@EnabledIfSystemProperty(named = "gateway.failure.url", matches = ".+")
class GatewayStressTest {
    static Stream<String> models() { return Stream.of("codex", "copilot"); }

    private List<Long> pids() throws Exception {
        try (var files = Files.list(Path.of(System.getProperty("gateway.failure.pids")))) {
            return files.map(path -> Long.parseLong(path.getFileName().toString())).toList();
        }
    }

    private long aliveProcesses() throws Exception {
        return pids().stream().filter(pid -> ProcessHandle.of(pid).map(ProcessHandle::isAlive).orElse(false)).count();
    }

    private long descriptors(long pid) throws Exception {
        try (var files = Files.list(Path.of("/proc", Long.toString(pid), "fd"))) {
            return files.count();
        }
    }

    private void awaitNoProcesses() throws Exception {
        var deadline = System.nanoTime() + Duration.ofSeconds(2).toNanos();
        while (System.nanoTime() < deadline) {
            if (aliveProcesses() == 0) return;
            Thread.sleep(10);
        }
        fail("Fixture CLI processes accumulated: " + aliveProcesses());
    }

    private void assertNormal(ChatClient client) {
        assertEquals("hello world", client.prompt().user("normal").call().content());
    }

    private Disposable hold(ChatClient client, CountDownLatch ready, List<Throwable> errors) {
        return client.prompt().user("hold").stream().content().subscribe(text -> {
            if (text.equals("ready")) ready.countDown();
        }, errors::add);
    }

    private void cancelRequest(ChatClient client) throws Exception {
        var ready = new CountDownLatch(1);
        var errors = new CopyOnWriteArrayList<Throwable>();
        var request = hold(client, ready, errors);
        try {
            assertTrue(ready.await(2, TimeUnit.SECONDS), "Cancellation fixture did not become ready: " + errors);
            assertTrue(errors.isEmpty(), "Unexpected failure: " + errors);
        }
        finally {
            request.dispose();
        }
        awaitNoProcesses();
    }

    private void overloadBurst(ChatClient client) throws Exception {
        var ready = new CountDownLatch(2);
        var errors = new CopyOnWriteArrayList<Throwable>();
        var holds = new ArrayList<Disposable>();
        var before = pids().size();
        try {
            for (int i = 0; i < 2; i++) holds.add(hold(client, ready, errors));
            assertTrue(ready.await(2, TimeUnit.SECONDS), "Two admitted requests did not become ready: " + errors);
            assertEquals(2, aliveProcesses(), "Configured concurrency was not respected");
            try (var executor = Executors.newVirtualThreadPerTaskExecutor()) {
                var start = new CountDownLatch(1);
                var rejected = IntStream.range(0, 8).mapToObj(index -> executor.submit(() -> {
                    start.await();
                    return assertThrows(RateLimitException.class,
                        () -> client.prompt().user("normal").call().content());
                })).toList();
                start.countDown();
                for (var future : rejected) {
                    var error = future.get(5, TimeUnit.SECONDS);
                    assertEquals(429, error.statusCode());
                    assertEquals("gateway_busy", error.code().orElseThrow());
                }
            }
            assertEquals(before + 2, pids().size(), "An overloaded request spawned a CLI");
            assertEquals(2, aliveProcesses(), "Original requests were lost during overload");
            assertTrue(errors.isEmpty(), "Admitted streams failed during overload: " + errors);
        }
        finally {
            holds.forEach(Disposable::dispose);
        }
        awaitNoProcesses();
    }

    @ParameterizedTest
    @MethodSource("models")
    void repeatedFailuresAndOverloadDoNotAccumulateResources(String model) throws Exception {
        try (var server = new FixtureGateway(model, "acceptance", 2);
             var context = new SpringApplicationBuilder(GatewayTest.Application.class)
                .web(WebApplicationType.NONE).run("--spring.ai.openai.base-url=" + server.url(),
                    "--spring.ai.openai.chat.model=" + model, "--spring.ai.openai.timeout=10s",
                    "--spring.main.banner-mode=off", "--logging.level.root=WARN",
                    "--logging.level.org.springframework.ai.chat.model.MessageAggregator=OFF")) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            // Warm both HTTP connection pools before taking a descriptor baseline.
            assertNormal(client);
            assertEquals("hello world", client.prompt().user("normal").stream().content()
                .reduce("", String::concat).block(Duration.ofSeconds(10)));
            awaitNoProcesses();
            long baseline = descriptors(server.pid());
            long maximum = baseline;
            var samples = new ArrayList<Long>();
            int requests = 2;
            for (int round = 0; round < 4; round++) {
                assertNormal(client);
                assertEquals("hello world", client.prompt().user("normal").stream().content()
                    .reduce("", String::concat).block(Duration.ofSeconds(10)));
                var failure = assertThrows(OpenAIServiceException.class,
                    () -> client.prompt().user("crash").call().content());
                assertEquals(502, failure.statusCode());
                assertEquals("harness_error", failure.code().orElseThrow());
                var partial = new CopyOnWriteArrayList<String>();
                var streamFailure = assertThrows(SseException.class, () -> client.prompt().user("crash")
                    .stream().content().doOnNext(partial::add).then().block(Duration.ofSeconds(10)));
                assertEquals("harness_error", streamFailure.code().orElseThrow());
                assertTrue(partial.contains("ready"), "Lost text before the injected crash");
                awaitNoProcesses();
                cancelRequest(client);
                overloadBurst(client);
                assertNormal(client);
                awaitNoProcesses();
                requests += 16;
                var current = descriptors(server.pid());
                samples.add(current);
                maximum = Math.max(maximum, current);
                // The ten-client burst may retain pooled inbound connections.
                // Bound that allowance rather than demanding an exact FD count.
                assertTrue(current <= baseline + 12,
                    "Descriptor count grew beyond the connection-pool allowance: " + baseline + " -> " + current);
            }
            assertTrue(samples.getLast() <= samples.getFirst() + 2,
                "Descriptors continued accumulating after the first overload burst: " + samples);
            System.out.printf("Fixture stress %s: requests=%d, peak active CLIs=2, FDs baseline=%d max=%d rounds=%s%n",
                model, requests, baseline, maximum, samples);
        }
    }
}
