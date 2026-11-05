package io.harnessgateway;

import java.time.Duration;
import java.util.Arrays;
import java.util.List;
import java.util.stream.IntStream;
import java.util.stream.Stream;

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
import org.springframework.boot.SpringBootConfiguration;
import org.springframework.boot.WebApplicationType;
import org.springframework.boot.autoconfigure.EnableAutoConfiguration;
import org.springframework.boot.builder.SpringApplicationBuilder;
import org.springframework.context.ConfigurableApplicationContext;
import org.springframework.context.annotation.Import;

import com.openai.errors.BadRequestException;

import static org.junit.jupiter.api.Assertions.*;

/** Live acceptance: Boot auto-configuration -> Spring AI -> gateway -> local CLI. */
class GatewayTest {

    @SpringBootConfiguration
    @EnableAutoConfiguration
    @Import(GatewayHttpConfiguration.class)
    static class Application {
    }

    static Stream<String> models() {
        return Arrays.stream(System.getProperty("gateway.models", "codex,copilot").split(","));
    }

    private ConfigurableApplicationContext application(String alias) {
        return new SpringApplicationBuilder(Application.class)
            .web(WebApplicationType.NONE)
            .run("--spring.ai.openai.chat.model=" + alias);
    }

    private ChatClient client(ConfigurableApplicationContext context) {
        return context.getBean(ChatClient.Builder.class).build();
    }

    @ParameterizedTest
    @MethodSource("models")
    void blockingChatClient(String alias) {
        try (var context = application(alias)) {
            var content = client(context).prompt()
                .system("Follow the requested output format exactly. Do not use tools.")
                .user("Reply with exactly GATEWAY_OK")
                .call().content();
            assertNotNull(content);
            assertEquals("GATEWAY_OK", content.trim());
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void streamingChatClient(String alias) {
        try (var context = application(alias)) {
            var responses = client(context).prompt()
                .system("Follow the requested output format exactly. Do not use tools.")
                .user("Write exactly the integers 1 through 80, separated by spaces, with no other text.")
                .stream().chatResponse().collectList().block(Duration.ofMinutes(3));
            assertNotNull(responses);
            var textChunks = responses.stream()
                .filter(response -> response.getResult() != null)
                .map(response -> response.getResult().getOutput().getText())
                .filter(text -> text != null && !text.isEmpty())
                .toList();
            assertTrue(textChunks.size() > 1, "Expected incremental text, not one buffered response");
            var expected = String.join(" ", IntStream.rangeClosed(1, 80).mapToObj(String::valueOf).toList());
            assertEquals(expected, String.join("", textChunks).trim().replaceAll("\\s+", " "));
            assertTrue(responses.stream().anyMatch(response ->
                response.getMetadata().getUsage().getTotalTokens() > 0),
                "Expected backend-reported usage when streaming usage is requested");
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void conversationHistoryAndUsage(String alias) {
        try (var context = application(alias)) {
            var response = context.getBean(OpenAiChatModel.class).call(new Prompt(List.of(
                new SystemMessage("Answer questions about the provided conversation. Do not use tools."),
                new UserMessage("Remember the code word: BLUEBIRD"),
                new AssistantMessage("I will remember BLUEBIRD."),
                new UserMessage("What was the code word? Reply with only that word.")
            )));
            assertNotNull(response);
            assertEquals("BLUEBIRD", response.getResult().getOutput().getText().trim());
            assertEquals("STOP", response.getResult().getMetadata().getFinishReason().toUpperCase());
            assertTrue(response.getMetadata().getUsage().getTotalTokens() > 0,
                "The authenticated CLI should report usage for this inference");
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void reasoningEffort(String alias) {
        try (var context = application(alias)) {
            var content = client(context).prompt()
                .options(OpenAiChatOptions.builder().reasoningEffort("high"))
                .user("Reply with exactly EFFORT_OK. Do not use tools.")
                .call().content();
            assertNotNull(content);
            assertEquals("EFFORT_OK", content.trim());
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void chatMemoryAcrossRequests(String alias) {
        try (var context = application(alias)) {
            var memory = MessageWindowChatMemory.builder().build();
            var client = context.getBean(ChatClient.Builder.class)
                .defaultSystem("Follow the requested output format exactly. Do not use tools.")
                .defaultAdvisors(MessageChatMemoryAdvisor.builder(memory).build())
                .build();
            var conversationId = "acceptance-" + alias;
            var first = client.prompt()
                .advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, conversationId))
                .user("Remember the code word BLUEBIRD. Reply with exactly SAVED.")
                .call().content();
            assertNotNull(first);
            assertEquals("SAVED", first.trim());
            var second = client.prompt()
                .advisors(spec -> spec.param(ChatMemory.CONVERSATION_ID, conversationId))
                .user("What was the code word? Reply with only that word.")
                .call().content();
            assertNotNull(second);
            assertEquals("BLUEBIRD", second.trim());
        }
    }

    @ParameterizedTest
    @MethodSource("models")
    void unsupportedOptionsSurfaceAsBadRequest(String alias) {
        try (var context = application(alias)) {
            var error = assertThrows(BadRequestException.class, () -> client(context).prompt()
                .options(OpenAiChatOptions.builder().temperature(0.5))
                .user("This request must fail validation before starting inference.")
                .call().content());
            assertEquals(400, error.statusCode());
            assertTrue(error.getMessage().contains("temperature"), error::getMessage);
        }
    }
}
