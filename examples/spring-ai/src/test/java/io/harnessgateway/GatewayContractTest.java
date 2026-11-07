package io.harnessgateway;

import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.file.Files;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.function.Consumer;
import java.util.stream.Stream;

import com.openai.errors.BadRequestException;
import com.openai.errors.NotFoundException;
import org.junit.jupiter.api.Timeout;
import org.junit.jupiter.api.condition.EnabledIfSystemProperty;
import org.junit.jupiter.params.ParameterizedTest;
import org.junit.jupiter.params.provider.MethodSource;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.client.advisor.MessageChatMemoryAdvisor;
import org.springframework.ai.chat.memory.ChatMemory;
import org.springframework.ai.chat.memory.MessageWindowChatMemory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.SystemMessage;
import org.springframework.ai.chat.messages.UserMessage;
import org.springframework.ai.chat.prompt.Prompt;
import org.springframework.ai.openai.OpenAiChatModel;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.boot.WebApplicationType;
import org.springframework.boot.builder.SpringApplicationBuilder;
import org.springframework.boot.json.JsonParserFactory;
import org.springframework.context.ConfigurableApplicationContext;

import static org.junit.jupiter.api.Assertions.*;

/** Boot -> Spring AI -> HTTP -> real adapters -> deterministic protocol fixtures. */
@Timeout(60)
@EnabledIfSystemProperty(named = "gateway.failure.url", matches = ".+")
class GatewayContractTest {
    static Stream<String> models() { return Stream.of("codex", "copilot"); }

    static ConfigurableApplicationContext application(String model, String url, String... properties) {
        var args = new java.util.ArrayList<>(List.of("--spring.ai.openai.chat.model=" + model,
            "--spring.ai.openai.base-url=" + url, "--spring.ai.openai.timeout=10s",
            "--spring.main.banner-mode=off", "--logging.level.root=WARN",
            "--logging.level.org.springframework.ai.chat.model.MessageAggregator=OFF"));
        for (var property : properties) {
            int separator = property.indexOf('=');
            if (separator > 0) args.removeIf(arg -> arg.startsWith(property.substring(0, separator + 1)));
            args.add(property);
        }
        return new SpringApplicationBuilder(GatewayTest.Application.class).web(WebApplicationType.NONE)
            .run(args.toArray(String[]::new));
    }

    private Map<String, Object> record(String content) {
        assertNotNull(content);
        return JsonParserFactory.getJsonParser().parseMap(content);
    }

    @ParameterizedTest
    @MethodSource("models")
    void modelOverridesAndReasoningDoNotLeakBetweenRequests(String model) throws Exception {
        try (var server = new FixtureGateway(model, "contract", 1, "text", "--" + model + "-model", "configured-default");
             var context = application(model, server.url())) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            assertEquals("configured-default", record(client.prompt().user("first").call().content()).get("model"));
            for (var effort : List.of("none", "minimal", "low", "medium", "high", "xhigh", "max")) {
                var captured = record(client.prompt().options(OpenAiChatOptions.builder()
                    .model(model + "/native-two").reasoningEffort(effort)).user("override").call().content());
                assertEquals("native-two", captured.get("model"));
                assertEquals(effort, captured.get("reasoning_effort"));
            }
            var last = record(client.prompt().user("last").call().content());
            assertEquals("configured-default", last.get("model"));
            assertEquals("", last.get("reasoning_effort"));
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void unicodeAndConversationRolesReachTheHarness(String model) throws Exception {
        var text = "Unicode Ω 日本語 😀\nquotes \"\\\"\r\n%PATH% ! $HOME & <text>";
        try (var server = new FixtureGateway(model, "contract"); var context = application(model, server.url())) {
            var chat = context.getBean(OpenAiChatModel.class);
            var direct = chat.call(new Prompt(List.of(new SystemMessage("First instruction"), new SystemMessage("Second instruction"), new UserMessage(text))));
            var captured = record(direct.getResult().getOutput().getText());
            assertEquals("First instruction\n\nSecond instruction", captured.get("instructions"));
            assertEquals(text, captured.get("input"));
            var response = chat.call(new Prompt(List.of(new SystemMessage("Be brief"), new UserMessage(text),
                new AssistantMessage("Previous answer Ω"), new UserMessage("Continue"))));
            var transcript = (String) record(response.getResult().getOutput().getText()).get("input");
            var history = JsonParserFactory.getJsonParser().parseList(transcript.substring(transcript.indexOf("\n\n") + 2));
            assertEquals(List.of(Map.of("role", "user", "content", text),
                Map.of("role", "assistant", "content", "Previous answer Ω"), Map.of("role", "user", "content", "Continue")), history);
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void metadataUsageAndFinalOnlyStreamingAreCompatible(String model) throws Exception {
        try (var server = new FixtureGateway(model, "fallback"); var context = application(model, server.url())) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            var response = client.prompt().user("normal").call().chatResponse();
            assertNotNull(response);
            assertTrue(response.getMetadata().getId().startsWith("chatcmpl-"));
            assertEquals(model, response.getMetadata().getModel());
            assertEquals(12, response.getMetadata().getUsage().getPromptTokens());
            assertEquals(3, response.getMetadata().getUsage().getCompletionTokens());
            assertEquals(15, response.getMetadata().getUsage().getTotalTokens());
            assertEquals(model.equals("codex") ? "STOP" : "LENGTH", response.getResult().getMetadata().getFinishReason().toUpperCase());
            for (var usage : List.of(true, false)) {
                var chunks = client.prompt().options(OpenAiChatOptions.builder().streamUsage(usage))
                    .user("normal").stream().chatResponse().collectList().block(Duration.ofSeconds(10));
                assertNotNull(chunks);
                assertEquals("hello world", chunks.stream().filter(chunk -> chunk.getResult() != null)
                    .map(chunk -> chunk.getResult().getOutput().getText()).filter(java.util.Objects::nonNull).reduce("", String::concat));
                assertEquals(usage, chunks.stream().anyMatch(chunk -> chunk.getMetadata().getUsage().getTotalTokens() > 0));
                var ids = chunks.stream().map(chunk -> chunk.getMetadata().getId()).filter(java.util.Objects::nonNull).distinct().toList();
                assertEquals(1, ids.size(), "SSE frames must retain the completion ID");
            }
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void memoryIsIsolatedAndCanBeCleared(String model) throws Exception {
        try (var server = new FixtureGateway(model, "contract", 2); var context = application(model, server.url())) {
            var memory = MessageWindowChatMemory.builder().maxMessages(4).build();
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief")
                .defaultAdvisors(MessageChatMemoryAdvisor.builder(memory).build()).build();
            client.prompt().advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, "A")).user("ALPHA_SECRET").call().content();
            var isolated = record(client.prompt().advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, "B"))
                .user("BETA_SECRET").call().content());
            assertEquals("BETA_SECRET", isolated.get("input"));
            var recalled = (String) record(client.prompt().advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, "A"))
                .user("recall").call().content()).get("input");
            assertTrue(recalled.contains("ALPHA_SECRET"));
            assertFalse(recalled.contains("BETA_SECRET"));
            var streamed = record(client.prompt().advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, "B"))
                .user("B recall").stream().content().reduce("", String::concat).block(Duration.ofSeconds(10)));
            assertTrue(((String) streamed.get("input")).contains("BETA_SECRET"));
            assertFalse(((String) streamed.get("input")).contains("ALPHA_SECRET"));
            client.prompt().advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, "A")).user("third turn").call().content();
            assertTrue(memory.get("A").size() <= 4, "Configured memory window was exceeded");
            assertTrue(memory.get("A").stream().noneMatch(message -> message instanceof UserMessage
                && message.getText().equals("ALPHA_SECRET")), "Old user turn was not evicted");
            try (var executor = java.util.concurrent.Executors.newVirtualThreadPerTaskExecutor()) {
                var concurrent = List.of("C", "D").stream().map(id -> executor.submit(() -> {
                    return record(client.prompt().advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, id))
                        .user("isolated-" + id).call().content()).get("input");
                })).toList();
                assertEquals("isolated-C", concurrent.get(0).get(10, java.util.concurrent.TimeUnit.SECONDS));
                assertEquals("isolated-D", concurrent.get(1).get(10, java.util.concurrent.TimeUnit.SECONDS));
            }
            memory.clear("A");
            assertEquals("fresh", record(client.prompt().advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, "A"))
                .user("fresh").call().content()).get("input"));
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void unsupportedSpringOptionsFailBeforeStartingCli(String model) throws Exception {
        try (var server = new FixtureGateway(model, "contract"); var context = application(model, server.url())) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            long before;
            try (var files = Files.list(server.pidsDirectory())) { before = files.count(); }
            List<Consumer<OpenAiChatOptions.Builder>> unsupported = List.of(
                builder -> builder.temperature(0.5), builder -> builder.topP(0.5),
                builder -> builder.maxCompletionTokens(16), builder -> builder.stop(List.of("STOP")),
                builder -> builder.seed(42), builder -> builder.n(2), builder -> builder.store(true),
                builder -> builder.metadata(Map.of("key", "value")), builder -> builder.reasoningEffort("invalid"));
            for (var configure : unsupported) {
                var builder = OpenAiChatOptions.builder();
                configure.accept(builder);
                var error = assertThrows(BadRequestException.class,
                    () -> client.prompt().options(builder).user("reject").call().content());
                assertEquals(400, error.statusCode());
            }
            var missing = assertThrows(NotFoundException.class, () -> client.prompt()
                .options(OpenAiChatOptions.builder().model("missing")).user("reject").call().content());
            assertEquals(404, missing.statusCode());
            try (var files = Files.list(server.pidsDirectory())) {
                assertEquals(before, files.count(), "Rejected options or model spawned a CLI");
            }
            assertEquals("normal", record(client.prompt().user("normal").call().content()).get("input"));
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void clientTimeoutStopsCliWithoutRetryAndRecovers(String model) throws Exception {
        try (var server = new FixtureGateway(model, "acceptance");
             var context = application(model, server.url(), "--spring.ai.openai.timeout=1s")) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            assertEquals("hello world", client.prompt().user("normal").call().content());
            for (var streaming : List.of(false, true)) {
                long before;
                try (var files = Files.list(server.pidsDirectory())) { before = files.count(); }
                var started = System.nanoTime();
                var timeout = assertThrows(RuntimeException.class, () -> {
                    if (streaming) client.prompt().user("silent").stream().content().then().block(Duration.ofSeconds(5));
                    else client.prompt().user("silent").call().content();
                });
                boolean timedOut = false;
                for (Throwable cause = timeout; cause != null; cause = cause.getCause()) {
                    var message = (cause.getClass().getName() + " " + cause.getMessage()).toLowerCase(java.util.Locale.ROOT);
                    timedOut |= message.contains("timeout") || message.contains("timed out");
                }
                assertTrue(timedOut, "Expected a client transport timeout: " + timeout);
                assertTrue(System.nanoTime() - started < Duration.ofSeconds(5).toNanos(), "Client timeout waited for the gateway's 30s timeout");
                var deadline = System.nanoTime() + Duration.ofSeconds(3).toNanos();
                boolean alive;
                do {
                    try (var files = Files.list(server.pidsDirectory())) {
                        var pids = files.toList();
                        assertEquals(before + 1, pids.size(), "Disabled retries still spawned another CLI");
                        alive = pids.stream().map(path -> Long.parseLong(path.getFileName().toString()))
                            .anyMatch(pid -> ProcessHandle.of(pid).map(ProcessHandle::isAlive).orElse(false));
                    }
                    if (alive) Thread.sleep(20);
                } while (alive && System.nanoTime() < deadline);
                assertFalse(alive, "Client timeout left its CLI running");
                assertEquals("hello world", client.prompt().user("normal").call().content());
            }
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void explicitlyEnabledRetryRepeatsOnlyTheFailedRequest(String model) throws Exception {
        try (var server = new FixtureGateway(model, "acceptance");
             var context = application(model, server.url(), "--spring.ai.openai.max-retries=1")) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            long before;
            try (var files = Files.list(server.pidsDirectory())) { before = files.count(); }
            assertEquals(502, assertThrows(com.openai.errors.OpenAIServiceException.class,
                () -> client.prompt().user("crash").call().content()).statusCode());
            try (var files = Files.list(server.pidsDirectory())) {
                assertEquals(before + 2, files.count(), "Expected one initial attempt and one explicit retry");
            }
            assertEquals("hello world", client.prompt().user("normal").call().content());
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void discoveryAndTextPartsWorkThroughJavaHttpClient(String model) throws Exception {
        try (var server = new FixtureGateway(model, "contract"); var http = HttpClient.newHttpClient()) {
            var response = http.send(HttpRequest.newBuilder(URI.create(server.url() + "/models")).GET().build(), HttpResponse.BodyHandlers.ofString());
            assertEquals(200, response.statusCode());
            var catalog = JsonParserFactory.getJsonParser().parseMap(response.body());
            var entries = (List<?>) catalog.get("data");
            assertEquals(3, entries.size());
            assertEquals(java.util.Set.of(model, model + "/native-one", model + "/native-two"),
                entries.stream().map(entry -> ((Map<?, ?>) entry).get("id")).collect(java.util.stream.Collectors.toSet()));
            var body = "{\"model\":\"" + model + "\",\"n\":1,\"messages\":[{\"role\":\"developer\",\"content\":\"Be brief\"},{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"Hello \"},{\"type\":\"text\",\"text\":\"Ω\"}]}]}";
            var completion = http.send(HttpRequest.newBuilder(URI.create(server.url() + "/chat/completions"))
                .header("Content-Type", "application/json").POST(HttpRequest.BodyPublishers.ofString(body)).build(), HttpResponse.BodyHandlers.ofString());
            assertEquals(200, completion.statusCode(), completion::body);
            var result = JsonParserFactory.getJsonParser().parseMap(completion.body());
            var choice = (Map<?, ?>) ((List<?>) result.get("choices")).getFirst();
            var message = (Map<?, ?>) choice.get("message");
            var captured = record((String) message.get("content"));
            assertEquals("Be brief", captured.get("instructions"));
            assertEquals("Hello Ω", captured.get("input"));
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void catalogExpiryFailureAndConcurrentRecoveryThroughHttp(String model) throws Exception {
        try (var server = new FixtureGateway(model, "catalog-gate"); var http = HttpClient.newHttpClient()) {
            server.releaseModelDiscovery();
            var before = server.startedCliCount();
            var request = HttpRequest.newBuilder(URI.create(server.url() + "/models"))
                .timeout(Duration.ofSeconds(5)).build();
            var first = http.send(request, HttpResponse.BodyHandlers.ofString());
            assertEquals(200, first.statusCode(), first::body);
            assertEquals("no-store", first.headers().firstValue("Cache-Control").orElseThrow());
            assertEquals(before + 1, server.startedCliCount());

            server.failModelDiscovery();
            var headRequest = HttpRequest.newBuilder(request.uri()).timeout(Duration.ofSeconds(5))
                .method("HEAD", HttpRequest.BodyPublishers.noBody()).build();
            var head = http.send(headRequest, HttpResponse.BodyHandlers.ofString());
            assertEquals(200, head.statusCode());
            assertEquals("", head.body());
            var warm = http.send(request, HttpResponse.BodyHandlers.ofString());
            assertEquals(first.body(), warm.body());
            assertEquals(before + 1, server.startedCliCount(), "Warm GET/HEAD restarted discovery");

            // Exercise the real server clock and CLI lifecycle without adding
            // a production cache-TTL flag or test-only HTTP endpoint.
            Thread.sleep(Duration.ofSeconds(31));
            var expired = http.send(request, HttpResponse.BodyHandlers.ofString());
            assertEquals(502, expired.statusCode(), expired::body);
            var error = (Map<?, ?>) record(expired.body()).get("error");
            assertEquals("model_discovery_failed", error.get("code"));
            assertEquals("server_error", error.get("type"));
            assertFalse(expired.body().contains("native-one"), "Expired catalog was served after refresh failure");
            assertEquals(before + 2, server.startedCliCount(), "Expired catalog did not attempt refresh");

            server.releaseModelDiscovery();
            var recovering = java.util.stream.IntStream.range(0, 16)
                .mapToObj(i -> http.sendAsync(request, HttpResponse.BodyHandlers.ofString())).toList();
            for (var response : recovering) {
                var result = response.get(8, java.util.concurrent.TimeUnit.SECONDS);
                assertEquals(200, result.statusCode(), result::body);
                assertEquals(first.body(), result.body());
            }
            assertEquals(before + 3, server.startedCliCount(), "Recovery was cached as a failure or restarted per client");
            head = http.send(headRequest, HttpResponse.BodyHandlers.ofString());
            assertEquals(200, head.statusCode());
            assertEquals("", head.body());
            assertEquals(before + 3, server.startedCliCount(), "Recovered HEAD did not use the new cache");
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void unsupportedWireFeaturesReturnErrorsWithoutStartingCli(String model) throws Exception {
        try (var server = new FixtureGateway(model, "contract"); var http = HttpClient.newHttpClient()) {
            long before;
            try (var files = Files.list(server.pidsDirectory())) { before = files.count(); }
            var validMessage = "{\"role\":\"user\",\"content\":\"normal\"}";
            var prefix = "{\"model\":\"" + model + "\",\"messages\":[";
            var bodies = List.of(
                prefix + "{\"role\":\"assistant\",\"content\":\"last assistant\"}]}",
                prefix + validMessage + ",{\"role\":\"system\",\"content\":\"late\"}]}",
                prefix + "{\"role\":\"user\",\"content\":[{\"type\":\"image_url\",\"image_url\":{\"url\":\"https://example.invalid/image\"}}]}]}",
                prefix + "{\"role\":\"user\",\"content\":null}]}",
                prefix + validMessage + "],\"tools\":[]}",
                prefix + validMessage + "],\"response_format\":{\"type\":\"json_object\"}}",
                prefix + validMessage + "],\"stream_options\":{\"include_usage\":true}}",
                prefix + validMessage + "],\"stream\":true,\"stream_options\":{\"include_obfuscation\":true}}",
                prefix + validMessage + "],\"unknown\":true}", "{invalid-json");
            for (var body : bodies) {
                var response = http.send(HttpRequest.newBuilder(URI.create(server.url() + "/chat/completions"))
                    .header("Content-Type", "application/json").POST(HttpRequest.BodyPublishers.ofString(body)).build(), HttpResponse.BodyHandlers.ofString());
                assertEquals(400, response.statusCode(), response::body);
                var error = (Map<?, ?>) JsonParserFactory.getJsonParser().parseMap(response.body()).get("error");
                assertEquals("invalid_request_error", error.get("type"));
                assertNotNull(error.get("code"));
            }
            try (var files = Files.list(server.pidsDirectory())) {
                assertEquals(before, files.count(), "Rejected wire requests spawned a CLI");
            }
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void largeUnicodeRequestsAndRequestLimitRecover(String model) throws Exception {
        try (var server = new FixtureGateway(model, "contract"); var context = application(model, server.url())) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            var text = "Ω".repeat(128 * 1024);
            assertEquals(text, record(client.prompt().user(text).call().content()).get("input"));
            long before;
            try (var files = Files.list(server.pidsDirectory())) { before = files.count(); }
            var oversized = assertThrows(com.openai.errors.OpenAIServiceException.class,
                () -> client.prompt().user("x".repeat(1024 * 1024)).call().content());
            assertEquals(413, oversized.statusCode());
            try (var files = Files.list(server.pidsDirectory())) {
                assertEquals(before, files.count(), "Oversized request spawned a CLI");
            }
            assertEquals("recovered", record(client.prompt().user("recovered").call().content()).get("input"));
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void escapedLargeResponsesDecodeInBlockingAndStreamingClients(String model) throws Exception {
        try (var server = new FixtureGateway(model, "escaped"); var context = application(model, server.url())) {
            var client = context.getBean(ChatClient.Builder.class).defaultSystem("Be brief").build();
            var blocking = client.prompt().user("normal").call().content();
            assertNotNull(blocking);
            assertEquals(3 * 1024 * 1024, blocking.length());
            assertTrue(blocking.chars().allMatch(value -> value == 0), "Escaped buffered text was altered");
            var streaming = client.prompt().user("normal").stream().content()
                .reduce("", String::concat).block(Duration.ofSeconds(30));
            assertNotNull(streaming);
            assertEquals(3 * 1024 * 1024, streaming.length());
            assertTrue(streaming.chars().allMatch(value -> value == 0), "Escaped streamed text was altered");
        }
    }
}
