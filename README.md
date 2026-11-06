# Harness-Gateway

A local, single-user Go gateway that exposes authenticated Codex and GitHub
Copilot CLIs through an OpenAI-compatible text Chat Completions API. It works
with Spring AI's OpenAI integration for blocking and streaming chat.

The gateway starts its own local CLI subprocesses, using the same OS user's
existing login and environment. It does not attach to an interactive terminal
session. No provider API key or gateway authentication is needed.

## Install

Use Go 1.27.3+ and the module package path, without `https://`:

```bash
go install github.com/harness-gateway/harness-gateway/cmd/harness-gateway@v0.1.4
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
bash setup.sh --version v0.1.4 --bin-dir "$HOME/.local/bin" --no-path
```

Without `--version`, the script selects `v0.1.4`. It requires Go to be installed.
It prints an export command for the current terminal; changing `~/.bashrc`
affects future Bash sessions. Existing CLI authentication stays managed by Codex
and Copilot. If another `harness-gateway` earlier on `PATH` shadows the installed
binary, setup prints its location and the export command to select this installation.
Install the release without cloning the repository:

```bash
curl -fsSL https://raw.githubusercontent.com/Harness-Gateway/harness-gateway/v0.1.4/setup.sh | bash
```

To download and inspect the script before running it:

```bash
curl -fsSL https://raw.githubusercontent.com/Harness-Gateway/harness-gateway/v0.1.4/setup.sh -o /tmp/harness-gateway-setup.sh
bash /tmp/harness-gateway-setup.sh
```

## Run

Requirements: `codex` and `copilot` on `PATH`, already logged in. Go 1.27.3+ is
needed for installation from source and development, not for a prebuilt binary.
The integration has been exercised with Codex CLI 0.166.0 and Copilot CLI 1.0.105.

```bash
harness-gateway
```

The release version is **v0.1.4**. See [release notes](CHANGELOG.md). To build a
local executable and inspect its version, source revision, and platform:

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
| — | `--log-format` | Choose `text` (default) or `json` logs |
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
request cleanup. Interrupted model discovery and buffered completions return HTTP
503 with code `gateway_shutting_down`; established SSE streams emit that error
without a successful finish chunk or `[DONE]`.

Model-discovery capacity rejections (HTTP 429) and shutdown interruptions (HTTP
503) are informational logs; CLI failures and discovery timeouts remain error logs.

## Startup status and logs

In an interactive terminal, startup displays a status panel with the gateway
version and build, repository, actual bound URLs, and a table of enabled harnesses:
installed CLI version, configured default model, and resolved executable. On
narrow terminals, executable paths move below their rows without being clipped.
The panel also shows working directory, concurrency, and timeout, and appears
after the listener binds successfully.

Startup queries each enabled executable with `--version`. Checks run in parallel,
are capped at two seconds each, and capture at most 4 KiB of output. Failed or
unrecognized version checks show `unavailable` and log a warning; they do not
prevent startup. Model settings show `CLI default` when no override was supplied.
Startup does not run inference or probe account authentication. Structured startup
logs include a `harness_versions` object for log collectors.

Colors follow terminal detection and are disabled by `NO_COLOR=1` or `TERM=dumb`.
Redirected output retains structured text logs. Use JSON logs for services or
log collectors; this also disables the terminal panel:

```bash
harness-gateway --log-format json
```

Logs go to stderr, including request IDs, durations, failures, and shutdown
events. `harness-gateway --version` prints its build identity to stdout.

## Interactive API

Open **<http://127.0.0.1:8080/docs>** to explore and test the API with the embedded
Scalar interface. Use the configured listener's port if you changed it. The
startup log includes the documentation URL. The OpenAPI document is available
at **`/openapi.json`**. Opening the gateway root (`/`) redirects to `/docs`.

1. Open **Discover available models**, select **Test Request**, and send the
   request to see the CLI aliases and available native model IDs.
2. Open **Create a chat completion** and choose a buffered or streaming example
   for Codex or Copilot. Edit the prompt or copy an exact model ID from discovery.
3. Send the request. Streaming examples set `stream: true`; cancel an active
   request to stop its CLI operation and release gateway capacity.

No API key is needed in the interface. Requests go directly to the same gateway
host and port using the CLIs' existing authentication. The pinned Scalar bundle,
specification, and UI assets are included in the Go binary; loading the interface
requires no npm, CDN, external fonts, or hosted request proxy. Provider inference
still uses the CLIs' normal network connections.

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
Model-discovery cases verify that overload spawns no extra CLI and SIGTERM returns
HTTP 503 while stopping the active discovery process.

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
| HTTP validation | Oversized bodies return 413, including trailing whitespace; unsupported methods on API endpoints return 405 with `Allow` |
| Local access | Host must be `localhost` or a loopback IP; cross-origin browser completion requests return 403. No auth is required for curl or Spring AI |
| Harness request errors | Explicit JSON-RPC invalid-parameter rejections and Copilot structured upstream HTTP 400 invalid-request errors return sanitized 400 errors; unclassified CLI failures return 502 |
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

The gateway owns harness CLI and version-probe process trees through Unix process
groups or Windows Job Objects in `internal/process`. Codex monitors parent exit
while draining stdout so inherited child pipes cannot hide a crash. The Copilot
SDK connects through a private, token-authenticated loopback bridge that checks
protocol headers (8 KiB) and frame
lengths before SDK allocation. Frames allow six times the 16 MiB decoded-text
budget plus 1 MiB of envelope overhead for JSON escaping. This internal connection
does not change the gateway's public API or CLI-managed authentication. The bridge
consumes its transport token before forwarding the SDK handshake to the stdio CLI,
including older installed versions without connection-token authentication.

## Develop

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race -p 1 ./...
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
Copilot regressions check descendant cleanup on success, cancellation, crashes,
and model-discovery failures; reject oversized frame headers before SDK allocation;
and exercise partial-frame cancellation and bridge authentication/recovery.

The command routes standard-log library diagnostics through the configured logger.
The SDK's known already-closed TCP writer diagnostic is classified as debug;
unexpected diagnostics remain visible as errors. Spring AI acceptance checks JSON
log records through normal, streaming, and failed Copilot requests.

### Windows launcher tests (unreleased)

Source builds support native Windows executables, npm `.cmd`/`.bat` entry points,
and `.js` entry points using Node.js. Batch wrappers remain inside the gateway's
Windows Job Object, so cancellation also terminates their native/Node children.
Prompts and model parameters travel over stdin rather than the shell command line.

Windows CI runs the backend and version-probe suites directly and again through
both `.cmd` and `.bat` wrappers. The fixtures cover discovery, buffered/SSE HTTP
responses, failures, cancellation, descendant cleanup, and paths containing spaces,
Unicode, percent signs, exclamation marks, and other shell metacharacters.
A separate Windows job installs pinned public Codex/Copilot npm packages and
tests their real entry points, version output, and protocol handshakes without
account credentials or inference. This does not qualify authenticated Windows
inference, which still requires a separate live acceptance run.

To run the installed-package smoke tests locally with existing CLIs:

```bash
HARNESS_GATEWAY_TEST_INSTALLED_CLIS=1 go test -v -count=1 -run '^TestInstalledCLI' ./cmd/harness-gateway ./internal/adapters/outbound/codex ./internal/adapters/outbound/copilot
```

Ordinary `go test ./...` skips these real-package tests. The JavaScript launcher
test uses Node.js when available; it skips when Node.js is absent.

GitHub Actions runs formatting, OpenAPI validation, generated-type reproducibility,
vet, tests, and builds on Linux, macOS, and Windows using the latest Go 1.27 patch.
Linux also runs the race detector and pinned `govulncheck` against reachable
vulnerabilities. Race packages run sequentially to bound large-fixture CPU and
memory peaks. Spring Boot acceptance runs on Linux and macOS with Java 27 and
Go CLI fixtures; the `/proc` descriptor stress checks run only on Linux.
An isolated Playwright browser job tests the Scalar interface against the same
Go CLI fixtures. It checks model discovery, buffered and streamed responses,
output before stream completion, cancellation and actual CLI process cleanup,
capacity recovery, error display, custom ports, mobile rendering, and absence of
off-origin requests from Scalar. A separate browser origin verifies no-CORS Blob
POSTs return 403 without starting a CLI. These tests need no logged-in harnesses. Reports, gateway
logs, and screenshots are uploaded as the `scalar-browser-acceptance` artifact.
Authenticated live acceptance remains a local gate, separate from CI.

Run the browser acceptance suite locally with Node.js 26+:

```bash
npm --prefix tools/browser ci --ignore-scripts
cd tools/browser
npx playwright install --only-shell chromium
npm test
```

Artifacts are written to `tools/browser/artifacts/`. Set
`HARNESS_GATEWAY_BROWSER_OUTPUT_DIR` to choose another directory, or
`HARNESS_GATEWAY_BROWSER_EXECUTABLE` to use an existing Chrome/Chromium executable.
Node.js and Playwright are development dependencies only; the gateway and its
embedded documentation do not require them at runtime.
On Linux hosts missing browser libraries, run `npx playwright install-deps chromium`.
CI uses the hosted Ubuntu runner's existing libraries and downloads the pinned
headless shell, with bounded installation and test durations.

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

The projection also defines the gateway's supported operations, constraints,
and runnable examples. Tests verify that the published document matches this
contract, references resolve locally, and all four completion examples pass
through the real HTTP handler. The pinned Scalar asset's checksum, license,
and update procedure are recorded in
[`docs/assets/README.md`](internal/adapters/inbound/openai/docs/assets/README.md).

Provider integration references:
[Codex App Server](https://learn.chatgpt.com/docs/app-server),
[Copilot Go SDK](https://github.com/github/copilot-sdk/tree/main/go),
[Spring AI OpenAI](https://docs.spring.io/spring-ai/reference/api/chat/openai-chat.html).

## License

Harness-Gateway is licensed under the [Apache License, Version 2.0](LICENSE).
The upstream OpenAI specification's MIT license is retained in
[`api/LICENSE.openai`](api/LICENSE.openai).
The bundled Scalar API Reference is MIT licensed; its license is retained in
[`LICENSE.scalar`](internal/adapters/inbound/openai/docs/assets/LICENSE.scalar)
and is available from the running gateway at `/docs/assets/LICENSE.scalar`.
