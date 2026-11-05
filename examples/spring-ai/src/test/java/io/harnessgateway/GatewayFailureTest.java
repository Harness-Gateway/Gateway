package io.harnessgateway;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.List;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;
import java.util.stream.Stream;

import com.openai.errors.OpenAIServiceException;
import com.openai.errors.RateLimitException;
import com.openai.errors.SseException;
import org.junit.jupiter.api.Timeout;
import org.junit.jupiter.api.condition.EnabledIfSystemProperty;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.Arguments;
import org.junit.jupiter.params.provider.MethodSource;

import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.model.ChatResponse;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.boot.WebApplicationType;
import org.springframework.boot.builder.SpringApplicationBuilder;
import org.springframework.context.ConfigurableApplicationContext;

import static org.junit.jupiter.api.Assertions.*;

/** Real HTTP and adapters, controlled Go CLI processes; no upstream inference. */
@Timeout(20)
@EnabledIfSystemProperty(named = "gateway.failure.url", matches = ".+")
class GatewayFailureTest {

    static Stream<String> models() {
        return Stream.of("codex", "copilot");
    }

    private ConfigurableApplicationContext application(String model) {
        return application(model, System.getProperty("gateway.failure.url"));
    }

    private ConfigurableApplicationContext application(String model, String url) {
        return new SpringApplicationBuilder(GatewayTest.Application.class)
            .web(WebApplicationType.NONE)
            .run("--spring.ai.openai.chat.model=" + model,
                "--spring.ai.openai.base-url=" + url,
                "--spring.ai.openai.timeout=10s", "--spring.main.banner-mode=off",
                "--logging.level.root=WARN",
                "--logging.level.org.springframework.ai.chat.model.MessageAggregator=OFF");
    }

    private ChatClient client(ConfigurableApplicationContext context) {
        return context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
    }

    private List<Long> pids() throws Exception {
        try (var files = Files.list(Path.of(System.getProperty("gateway.failure.pids")))) {
            return files.map(path -> Long.parseLong(path.getFileName().toString())).toList();
        }
    }

    private void awaitProcessesStopped() throws Exception {
        var deadline = System.nanoTime() + Duration.ofSeconds(2).toNanos();
        List<Long> alive;
        do {
            alive = pids().stream().filter(pid ->
                ProcessHandle.of(pid).map(ProcessHandle::isAlive).orElse(false)).toList();
            if (alive.isEmpty()) {
                return;
            }
            Thread.sleep(20);
        } while (System.nanoTime() < deadline);
        fail("CLI processes were not reaped before the gateway timeout: " + alive);
    }

    private void assertGatewayReusable(ChatClient client) {
        assertEquals("hello world", client.prompt().user("normal").call().content());
    }

    static Stream<Arguments> earlyCancellationCases() {
        return models().flatMap(model -> Stream.of(
            Arguments.of(model, "startup-hold"), Arguments.of(model, "silent")));
    }

    private void awaitNewProcess(int previousCount) throws Exception {
        var deadline = System.nanoTime() + Duration.ofSeconds(5).toNanos();
        while (System.nanoTime() < deadline) {
            if (pids().size() == previousCount + 1) {
                return;
            }
            Thread.sleep(20);
        }
        fail("Expected a request-owned CLI process to start");
    }

    @ParameterizedTest
    @MethodSource("earlyCancellationCases")
    void cancellationBeforeOutputStopsCli(String model, String phase) throws Exception {
        var mode = phase.equals("startup-hold") ? phase : "acceptance";
        try (var server = new FixtureGateway(model, mode); var context = application(model, server.url())) {
            var client = client(context);
            var count = pids().size();
            var text = new CopyOnWriteArrayList<String>();
            var errors = new CopyOnWriteArrayList<Throwable>();
            var subscription = client.prompt().user("silent").stream().content()
                .filter(chunk -> !chunk.isEmpty()).subscribe(text::add, errors::add);
            try {
                awaitNewProcess(count);
                if (phase.equals("silent")) {
                    server.awaitTurnStarted();
                }
                assertTrue(text.isEmpty(), "Unexpected assistant text before cancellation");
                assertTrue(errors.isEmpty(), "Request failed before cancellation: " + errors);
                assertTimeout(Duration.ofSeconds(2), () -> {
                    subscription.dispose();
                    awaitProcessesStopped();
                }, "Cancellation must not wait for the 30s gateway timeout");
                assertGatewayReusable(client);
                awaitProcessesStopped();
            }
            finally {
                subscription.dispose();
            }
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void shutdownInterruptsBlockingRequest(String model) throws Exception {
        try (var server = new FixtureGateway(model, "acceptance");
             var context = application(model, server.url());
             var executor = Executors.newVirtualThreadPerTaskExecutor()) {
            var client = client(context);
            var count = pids().size();
            var response = executor.submit(() -> client.prompt().user("hold").call().content());
            awaitNewProcess(count);
            server.awaitTurnStarted();
            server.terminate();
            var error = assertThrows(ExecutionException.class, () -> response.get(8, TimeUnit.SECONDS));
            var serviceError = assertInstanceOf(OpenAIServiceException.class, error.getCause());
            assertEquals(503, serviceError.statusCode());
            assertEquals("gateway_shutting_down", serviceError.code().orElseThrow());
            server.awaitExit();
            awaitProcessesStopped();
        }
    }

    static Stream<Arguments> streamingShutdownCases() {
        return models().flatMap(model -> Stream.of(
            Arguments.of(model, "silent"), Arguments.of(model, "hold")));
    }

    @ParameterizedTest
    @MethodSource("streamingShutdownCases")
    void shutdownFailsStreamWithoutSuccessfulCompletion(String model, String scenario) throws Exception {
        try (var server = new FixtureGateway(model, "acceptance"); var context = application(model, server.url())) {
            var client = client(context);
            var count = pids().size();
            var received = new CopyOnWriteArrayList<ChatResponse>();
            var error = new AtomicReference<Throwable>();
            var success = new AtomicBoolean();
            var ready = new CountDownLatch(1);
            var finished = new CountDownLatch(1);
            var subscription = client.prompt().user(scenario).stream().chatResponse().subscribe(response -> {
                received.add(response);
                if (response.getResult() != null && "ready".equals(response.getResult().getOutput().getText())) {
                    ready.countDown();
                }
            }, failure -> { error.set(failure); finished.countDown(); },
                () -> { success.set(true); finished.countDown(); });
            try {
                awaitNewProcess(count);
                if (scenario.equals("hold")) {
                    assertTrue(ready.await(2, TimeUnit.SECONDS), "Expected partial text before shutdown");
                }
                else {
                    server.awaitTurnStarted();
                }
                server.terminate();
                assertTrue(finished.await(8, TimeUnit.SECONDS), "Stream did not terminate after SIGTERM");
                assertFalse(success.get(), "An interrupted stream completed successfully");
                var failure = assertInstanceOf(SseException.class, error.get());
                assertEquals("gateway_shutting_down", failure.code().orElseThrow());
                assertTrue(received.stream().noneMatch(response -> response.getResult() != null
                    && "stop".equalsIgnoreCase(response.getResult().getMetadata().getFinishReason())),
                    "Shutdown produced a successful finish reason");
                server.awaitExit();
                awaitProcessesStopped();
            }
            finally {
                subscription.dispose();
            }
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void cancellingStreamStopsCliAndReleasesSlot(String model) throws Exception {
        try (var context = application(model)) {
            var client = client(context);
            var count = pids().size();
            var firstChunkAt = new AtomicLong();
            var chunks = client.prompt().user("hold").stream().content()
                .filter(text -> !text.isEmpty())
                .doOnNext(text -> firstChunkAt.compareAndSet(0, System.nanoTime()))
                .take(1).collectList().block(Duration.ofSeconds(10));
            assertEquals(List.of("ready"), chunks);
            assertTrue(System.nanoTime() - firstChunkAt.get() < Duration.ofSeconds(2).toNanos(),
                "Cancellation after the first chunk waited for the gateway's 4s deadline");
            assertEquals(count + 1, pids().size(), "Expected an actual CLI process");
            awaitProcessesStopped();
            assertGatewayReusable(client);
            awaitProcessesStopped();
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void concurrencyLimitSurfacesWithoutRetryOrExtraCli(String model) throws Exception {
        try (var context = application(model)) {
            var client = client(context);
            var ready = new CountDownLatch(1);
            var errors = new CopyOnWriteArrayList<Throwable>();
            var count = pids().size();
            var subscription = client.prompt().user("hold").stream().content()
                .subscribe(text -> { if (text.contains("ready")) ready.countDown(); }, errors::add);
            try {
                assertTrue(ready.await(2, TimeUnit.SECONDS), "CLI did not emit its first chunk: " + errors);
                var other = model.equals("codex") ? "copilot" : "codex";
                var error = assertThrows(RateLimitException.class, () -> client.prompt()
                    .options(OpenAiChatOptions.builder().model(other))
                    .user("normal").call().content());
                assertEquals(429, error.statusCode());
                assertEquals("gateway_busy", error.code().orElseThrow());
                assertEquals(count + 1, pids().size(), "Rejected request spawned a CLI");
                assertTrue(errors.isEmpty(), "The original stream failed: " + errors);
            }
            finally {
                assertTimeout(Duration.ofSeconds(2), () -> {
                    subscription.dispose();
                    awaitProcessesStopped();
                }, "Disposing a stream must stop its CLI before the gateway's 4s deadline");
            }
            awaitProcessesStopped();
            assertGatewayReusable(client);
            awaitProcessesStopped();
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void blockingTimeoutSurfacesAs504AndStopsCli(String model) throws Exception {
        assertBlockingFailure(model, "hold", 504, "harness_timeout");
    }

    @ParameterizedTest
    @MethodSource("models")
    void blockingCrashSurfacesAs502(String model) throws Exception {
        assertBlockingFailure(model, "crash", 502, "harness_error");
    }

    private void assertBlockingFailure(String model, String scenario, int status, String code) throws Exception {
        try (var context = application(model)) {
            var client = client(context);
            var error = assertThrows(OpenAIServiceException.class,
                () -> client.prompt().user(scenario).call().content());
            assertEquals(status, error.statusCode());
            assertEquals(code, error.code().orElseThrow());
            awaitProcessesStopped();
            assertGatewayReusable(client);
            awaitProcessesStopped();
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void streamingCrashEmitsErrorAfterPartialText(String model) throws Exception {
        assertStreamingFailure(model, "crash", "harness_error");
    }

    @ParameterizedTest
    @MethodSource("models")
    void streamingTimeoutEmitsErrorAfterPartialText(String model) throws Exception {
        assertStreamingFailure(model, "hold", "harness_timeout");
    }

    private void assertStreamingFailure(String model, String scenario, String code) throws Exception {
        try (var context = application(model)) {
            var client = client(context);
            var received = new CopyOnWriteArrayList<ChatResponse>();
            var error = assertThrows(SseException.class, () -> client.prompt().user(scenario)
                .stream().chatResponse().doOnNext(received::add).then().block(Duration.ofSeconds(10)));
            assertEquals(code, error.code().orElseThrow());
            assertTrue(received.stream().anyMatch(response -> response.getResult() != null
                && "ready".equals(response.getResult().getOutput().getText())), "Lost partial text");
            assertTrue(received.stream().noneMatch(response -> response.getResult() != null
                && "stop".equalsIgnoreCase(response.getResult().getMetadata().getFinishReason())),
                "A failed stream reported successful completion");
            awaitProcessesStopped();
            assertGatewayReusable(client);
            awaitProcessesStopped();
        }
    }
}
