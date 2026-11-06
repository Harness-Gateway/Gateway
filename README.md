# Harness-Gateway

A local, single-user Go gateway that exposes authenticated Codex and GitHub
Copilot CLIs through an OpenAI-compatible text Chat Completions API. It works
with Spring AI's OpenAI integration for blocking and streaming chat.

The gateway starts its own local CLI subprocesses, using the same OS user's
existing login and environment. It does not attach to an interactive terminal
session. No provider API key or gateway authentication is needed.

## Install

Use Go 1.25+ and the module package path, without `https://`:

```bash
go install github.com/harness-gateway/harness-gateway/cmd/harness-gateway@v0.1.1
```

This installs the command **`harness-gateway`** into `GOBIN`, or into
`$GOPATH/bin` (usually `~/go/bin`) when `GOBIN` is unset. Add that directory to
your `PATH`. Use the exact lower-case module path shown above.

To install a development snapshot:

```bash
go install github.com/harness-gateway/harness-gateway/cmd/harness-gateway@dev
```

Versioned installs require the corresponding Git tag to be published. Use `@dev`
for the development branch before a release is available.

For Bash, the convenience script installs the command into `GOBIN` or
`~/.local/bin`, and adds the directory to `~/.bashrc` once if needed:

```bash
bash setup.sh

# Install the development branch before a release is published.
bash setup.sh --version dev

# Select another module version or destination; avoid editing ~/.bashrc.
bash setup.sh --version v0.1.1 --bin-dir "$HOME/.local/bin" --no-path
```

Without `--version`, the script selects `v0.1.1`. It requires Go to be installed.
It prints an export command for the current terminal; changing `~/.bashrc`
affects future Bash sessions. Existing CLI authentication stays managed by Codex
and Copilot. Install the release without cloning the repository:

```bash
curl -fsSL https://raw.githubusercontent.com/Harness-Gateway/harness-gateway/v0.1.1/setup.sh | bash
```

To download and inspect the script before running it:

```bash
curl -fsSL https://raw.githubusercontent.com/Harness-Gateway/harness-gateway/v0.1.1/setup.sh -o /tmp/harness-gateway-setup.sh
bash /tmp/harness-gateway-setup.sh
```

## Run

Requirements: `codex` and `copilot` on `PATH`, already logged in. Go 1.25+ is
needed for installation from source and development, not for a prebuilt binary.
The integration has been exercised with Codex CLI 0.166.0 and Copilot CLI 1.0.105.

```bash
harness-gateway
```

The release version is **v0.1.1**. To build a local executable and inspect its
version, source revision, and platform:

```bash
go build -o harness-gateway ./cmd/harness-gateway
./harness-gateway --version
```

Version output works without either harness CLI installed. Builds from modified
sources include `+dirty` in their revision; release binaries use a clean checkout.

The default listener is `http://127.0.0.1:8080`. Only loopback listen addresses
are accepted. `GET /v1/models` discovers the CLIs' native model catalogs and
returns IDs such as `codex/<native-model-id>` and `copilot/<native-model-id>`.
Use one of these IDs in a completion's `model` field to select that exact model.
The short aliases `codex` and `copilot` remain available for each CLI's configured
default. CLI authentication remains managed by the CLI.

```bash
# Enable a single harness, set its native model and working directory.
harness-gateway --harnesses codex --codex-model YOUR_CODEX_MODEL --directory /path/to/project

# Set a native model for each of the two model aliases.
harness-gateway --codex-model YOUR_CODEX_MODEL --copilot-model YOUR_COPILOT_MODEL
```

Use `--help` (or `-h`) for executable paths, listen address, timeout (default `3m`), and
concurrency (default `2`). The gateway passes a read-only sandbox and never-approve
policy to Codex. Copilot's SDK session has an empty tool allowlist and rejects
permission requests. Interactive questions and client-defined tools are unsupported.

Long options use `--`; their short aliases use `-` followed by one letter:

| Short | Long | Purpose |
| --- | --- | --- |
| `-h` | `--help` | Show help |
| `-v` | `--version` | Print version and build identity |
| `-l` | `--listen` | Set the loopback listen address |
| `-H` | `--harnesses` | Select enabled harnesses |
| `-d` | `--directory` | Set the CLI working directory |
| `-t` | `--timeout` | Set the completion timeout |
| `-c` | `--concurrency` | Set the maximum simultaneous completions |
| — | `--codex-path` | Select the Codex executable |
| — | `--copilot-path` | Select the Copilot executable |
| — | `--codex-model` | Set the default Codex model |
| — | `--copilot-model` | Set the default Copilot model |

For example, `harness-gateway -l 127.0.0.1:8090` is equivalent to
`harness-gateway --listen 127.0.0.1:8090`. Both `--option value` and
`--option=value` are accepted. Single-dash long spellings such as `-listen`
and `-version` are not supported; use `-l`/`--listen` and `-v`/`--version`.

The `--codex-model` and `--copilot-model` flags configure the short aliases only;
an explicit namespaced model ID overrides that default for its request. Discovery
uses Codex's documented `model/list` with pagination and the Copilot SDK's
`ListModels`. Hidden Codex models, models without text input, and explicitly
disabled Copilot models are omitted. A listed model remains subject to account
permissions and availability when inference runs. Discovery has a 20-second cap
(or the configured timeout if shorter); a discovery failure returns an error.
Discovery and completion requests share the configured concurrency limit; excess
requests receive HTTP 429 instead of spawning more CLI processes.

```bash
curl http://127.0.0.1:8080/v1/models

curl --fail-with-body --max-time 45 -sS http://127.0.0.1:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"codex","messages":[{"role":"user","content":"Say hello"}]}'

curl --fail-with-body --max-time 45 -N http://127.0.0.1:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"copilot","messages":[{"role":"user","content":"Say hello"}],"stream":true}'
```

Paste each curl command as one complete line. If Bash shows a `>` prompt, it is
waiting for an unfinished quote or line continuation; press Ctrl+C and paste the
complete command again. Normal chat waits for the full reply. Streaming emits
an initial role-only chunk after model routing and capacity admission, then text
as it arrives, and ends with `data: [DONE]`. Clients can cancel during CLI startup
or before the first text token. Admission failures retain their HTTP 404/429
responses; backend failures after SSE starts are stream errors. The gateway logs
request IDs and durations, and returns the ID in the `X-Request-ID` response header.

`GET /health` reports gateway liveness, not upstream authentication or model
availability. CLI availability is checked at startup; authentication is exercised
when a completion runs. The gateway does not retry failed completions.

On SIGINT or SIGTERM, the gateway cancels active CLI operations and waits for
request cleanup. Interrupted buffered completions return HTTP 503 with code
`gateway_shutting_down`; established SSE streams emit that error without a
successful finish chunk or `[DONE]`.

## Spring AI

The included acceptance project uses **Java 27**, **Spring AI 2.0.2**, and Spring
Boot **4.1.2** auto-configuration (Spring Framework **7.0.10**), the latest
stable combination verified on 2026-11-05. Configure its OpenAI client to use
the gateway's `/v1` base URL.
For a Spring Boot application with `spring-ai-starter-model-openai`:

```properties
spring.ai.openai.base-url=http://127.0.0.1:8080/v1
spring.ai.openai.api-key=local-placeholder
spring.ai.openai.chat.model=codex
spring.ai.openai.timeout=180s
spring.ai.openai.max-retries=0
spring.ai.openai.chat.stream-usage=true

# This gateway only implements chat. Disable the starter's other model beans.
spring.ai.model.embedding=none
spring.ai.model.image=none
spring.ai.model.audio.speech=none
spring.ai.model.audio.transcription=none
spring.ai.model.moderation=none
```

The placeholder satisfies clients that expect a key; the gateway does not validate
or forward it. Select `copilot` to use Copilot. Do not configure temperature,
token limits, tools, or structured output: those options are rejected explicitly.
For older Spring AI versions, follow their documented base-URL/path conventions
and clear any default temperature; they are not covered by this acceptance suite.

For prompt cancellation of an established SSE stream with Spring AI 2.0.2, import the included
[`GatewayHttpConfiguration`](examples/spring-ai/src/main/java/io/harnessgateway/GatewayHttpConfiguration.java)
into your Boot application (`@Import(GatewayHttpConfiguration.class)`). Its
`OpenAiHttpClientBuilderCustomizer` aborts the underlying HTTP call before closing
an SSE body. The default Spring AI transport can otherwise block disposal of an
idle stream until another chunk or the gateway timeout arrives. The acceptance
suite includes this configuration and checks cancellation before that timeout.

A standalone client can be constructed as follows:

```java
var model = OpenAiChatModel.builder()
    .options(OpenAiChatOptions.builder()
        .baseUrl("http://127.0.0.1:8080/v1")
        .apiKey("local-placeholder")
        .model("codex")
        .temperature(null)
        .maxRetries(0)
        .build())
    .build();
var client = ChatClient.create(model);
String answer = client.prompt().user("Say hello").call().content();
Flux<String> chunks = client.prompt().user("Say hello").stream().content();
```

Run repeatable Spring Boot acceptance from the repository root (Go, Java 27,
and Maven):

```bash
# Failure handling and stress with Go CLI fixtures; no upstream inference.
go run ./tools/acceptance

# Also test real inference using the authenticated local CLIs.
go run ./tools/acceptance -live

# Include explicit native model selection alongside the default aliases.
go run ./tools/acceptance -live -models codex,copilot,codex/gpt-6.1-sol,copilot/gpt-6.1-sol
```

The runner builds the gateway and Go CLI fixtures, starts owned gateways on
OS-assigned loopback ports, checks readiness, runs Maven, and stops the gateways
and removes temporary files on completion or interruption. It does not stop an
existing gateway. Surefire reports are kept under `examples/spring-ai/target/`.

To test an already running gateway directly:

```bash
mvn -f examples/spring-ai/pom.xml test

# Override the URL or test only one enabled harness.
mvn -f examples/spring-ai/pom.xml \
  -Dgateway.url=http://127.0.0.1:8080/v1 -Dgateway.models=codex test

# Exercise both aliases and explicit native model selection.
mvn -f examples/spring-ai/pom.xml -Dgateway.models=codex,copilot,codex/gpt-6.1-sol,copilot/gpt-6.1-sol test
```

The tests start a real Spring Boot context with `spring-ai-starter-model-openai`,
load [`application.properties`](examples/spring-ai/src/test/resources/application.properties),
import `GatewayHttpConfiguration`, and use the injected `ChatClient.Builder` and
`OpenAiChatModel` beans. Each harness
is tested through blocking calls, incremental streaming with token usage,
conversation history, a `MessageChatMemoryAdvisor` conversation across two
requests, high reasoning effort, and HTTP 400 propagation for unsupported options.
The reasoning test requires a model that supports `high`; choose compatible
CLI defaults or explicit native IDs when running it. These tests use the CLIs'
authenticated upstream inference and may consume the account's usage allowance.

The separate `GatewayFailureTest` suite runs only through the acceptance runner.
It uses the real HTTP server, adapters, and Spring Boot starter with Go fixture
CLIs to verify blocking 502/504 responses, SSE errors after partial text, stream
cancellation, and HTTP 429 across harnesses. Additional cases cancel during CLI
initialization and silent inference, and send SIGTERM during buffered and
streaming requests (before and after text). Tests check actual child PIDs are
reaped and a subsequent request succeeds, proving the concurrency slot is released.

On Linux, `GatewayStressTest` adds 66 requests per harness, mixing buffered and
streaming success, injected crashes, cancellation, recovery, and ten-client
overload bursts with a concurrency limit of two. It checks all owned CLI
processes exit, rejected requests spawn none, and the gateway's `/proc` descriptor
count stays within a bounded allowance for retained HTTP connections.

## Compatibility and limits

| Feature | MVP behavior |
| --- | --- |
| `GET /v1/models` | Discovers native model IDs, namespaced by harness, plus the default aliases |
| `POST /v1/chat/completions` | Buffered JSON or SSE with `stream=true` |
| Messages | `system`, `developer`, `user`, `assistant`; final message must be `user` |
| Content | Strings or nonempty arrays of `{ "type": "text", "text": "..." }` |
| Instructions | Initial system/developer messages combined into CLI session instructions |
| History | Prior user/assistant messages encoded as a role-labelled JSON transcript |
| Sessions | A fresh backend session/process for every request; no cross-request state |
| `n` | Omitted, null, or `1` |
| `reasoning_effort` | Forwarded to the CLI; support depends on the selected native model |
| `stream_options.include_usage` | Supported; backend-reported token counts only |
| `stream_options.include_obfuscation` | Omitted, null, or `false`; padding is unsupported |
| Usage | Optional; covers the harness run, including its own instructions/context |
| Limits | 1 MiB request; 16 MiB UTF-8 assistant text before JSON/SSE encoding; bounded concurrency and duration |
| Slow readers | Ten-second socket-write budget for buffered JSON and each SSE write |
| Cancellation | HTTP disconnect/deadline stops the request-owned CLI process |
| Other request fields | Rejected with HTTP 400 and an OpenAI-style error envelope |
| Responses, embeddings, images, audio, client tool calls | Unsupported |

This is an adaptation of coding harnesses, so it does not provide exact model-API
semantics. History is contextual text, not native role-preserving history import.
System/developer instructions must precede conversation messages and remain subject
to the harness's own instructions. No sampling parameters or token budgets are
silently ignored. SSE failures after output starts emit an error envelope and end
the stream without a successful finish chunk or `[DONE]`.

## Architecture

The implementation follows a small hexagonal architecture:

```text
Spring AI / OpenAI clients
          |
internal/adapters/inbound/openai   HTTP, validation, generated wire types, SSE
          |
internal/application              alias routing and concurrency
          |
internal/domain                   Harness completion port and neutral values
          |
internal/adapters/outbound         Codex app-server / official Copilot Go SDK
```

The application and domain packages do not import HTTP, provider SDKs, or process
protocols. Add a harness by implementing `domain.Harness` and registering its
model alias in `cmd/harness-gateway`. CLI-specific conversation adaptation stays outbound.

## Develop

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/harness-gateway
```

Go tests use fake harnesses and Go subprocess protocol fixtures, with no upstream
calls. Copilot tests cover crashes, startup and session errors, duplicate messages,
usage, cancellation, and cleanup. A local SDK ping detects a lost Copilot connection
without waiting for the inference deadline. The Maven suite is the separate live
Spring AI acceptance gate.

Transport tests accept exactly 16 MiB of UTF-8 text, reject an additional byte in
both response modes, and verify subsequent requests recover. Real TCP tests keep
clients connected without consuming the response body and check the ten-second
write budget releases both buffered and streaming handlers and backend capacity.

GitHub Actions runs formatting, vet, race tests, the Go build, and the Spring Boot
failure and stress acceptance suites with Java 27 and Go CLI fixtures.
Authenticated live acceptance remains a local gate, separate from CI.

API wire types are generated with pinned `oapi-codegen` from
[`api/openapi.json`](api/openapi.json), a narrowed projection of the
[official OpenAI OpenAPI specification](https://github.com/openai/openai-openapi).
Regenerate with `go generate ./api`; never edit `types.gen.go` manually.
The projection records the upstream commit and SHA-256. To reproduce the projection,
download the pinned `sourceURL` in `tools/project-openapi/main.go`, then run:

```bash
go run ./tools/project-openapi -input /path/to/downloaded/openapi.json
go generate ./api
```

Provider integration references:
[Codex App Server](https://learn.chatgpt.com/docs/app-server),
[Copilot Go SDK](https://github.com/github/copilot-sdk/tree/main/go),
[Spring AI OpenAI](https://docs.spring.io/spring-ai/reference/api/chat/openai-chat.html).

## License

Harness-Gateway is licensed under the [Apache License, Version 2.0](LICENSE).
The upstream OpenAI specification's MIT license is retained in
[`api/LICENSE.openai`](api/LICENSE.openai).
