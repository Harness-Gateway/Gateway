# Changelog

## Unreleased

- Expand the local Spring AI contract suite with deterministic request forwarding,
  model/reasoning overrides, response metadata, memory isolation, client timeouts,
  retry counts, and validation of unsupported options and wire features.
- Add opt-in Linux Spring AI soak testing with retained CSV samples for gateway
  RSS, post-GC heap/objects, goroutines, descriptors, and CLI process cleanup.
- Retire Copilot SDK session event consumers before deleting completed sessions;
  bound detach cleanup and preserve compatibility with CLIs lacking that method.

## v0.1.5 - 2026-11-06

- Accept an existing gateway executable in the Spring AI acceptance runner with `--gateway-binary`; run CI acceptance against a private Go-installed binary.
- Test versioned Go installation outside the checkout on Linux, macOS, and Windows, independently of the Unix Bash installer.
- Bound CI jobs and cancel superseded branch/PR runs without cancelling immutable tag runs.
- Support Windows `.cmd`/`.bat` CLI entry points through the shared owned-process launcher, and use Node.js consistently for `.js` entry points and version probes.
- Exercise Codex/Copilot protocol and lifecycle tests through native Windows batch shims, including paths with spaces, Unicode, and shell metacharacters.
- Add opt-in real-package version/protocol smoke tests and a credential-free Windows CI job with pinned Codex/Copilot npm packages.

## v0.1.4 - 2026-11-06

- Stop SSE output after a transport failure without renewing the write deadline for an error frame.
- Confirm process-group disappearance before suppressing macOS's transient zombie-group permission error during cleanup.
- Require Go 1.27.3 or newer and check reachable Go vulnerabilities in CI.
- Reject non-loopback Host headers and cross-origin browser completion requests;
  preserve unauthenticated curl, Spring AI, and same-origin Scalar clients.
- Preserve request timeout and cancellation causes when a CLI returns a transport
  error, including Codex blocked on stdin writes.
- Allow JSON-escaped Codex frames within the 16 MiB decoded assistant-text limit
  while keeping a bounded wire-frame size; deduplicate repeated final messages.
- Return sanitized HTTP 400 errors for explicit structured completion-parameter
  rejections, preserving generic CLI failures as HTTP 502.
- Warn when an earlier PATH entry shadows the binary installed by setup.sh.
- Share process-tree ownership across harnesses and CLI version probes, stopping
  descendants through Unix process groups and Windows Job Objects.
- Detect Codex parent exit promptly while draining buffered output, including
  crashes where a child retains stdout; preserve partial SSE text before errors.
- Guard Copilot frame and header sizes before SDK parsing through a private,
  token-authenticated loopback bridge; retain the official SDK for operations.
- Consume bridge credentials before the CLI handshake for installed-version
  compatibility, and use fixed copy buffers to avoid retained splice-pool pipes.
- Route standard-log library diagnostics through the configured logger, classify
  the benign SDK closed-writer message as debug, and retain unexpected errors.
- Add Linux, macOS, and Windows Go CI, Unix Spring AI acceptance, and a real-browser
  regression for cross-origin no-CORS requests.

## v0.1.3 - 2026-11-06

- Redirect the gateway root (`/`) to the interactive API documentation at `/docs`.
- Return HTTP 413 when trailing whitespace exceeds the 1 MiB request limit,
  including streamed request bodies.
- Return OpenAI-style HTTP 405 errors with an `Allow` header for unsupported
  methods on API endpoints; unknown paths retain HTTP 404 errors.
- Return HTTP 503 with `gateway_shutting_down` when shutdown interrupts model
  discovery, and stop the discovery CLI before the gateway exits.
- Log expected model-discovery capacity rejections and shutdown interruptions
  at informational level; retain error logs for CLI failures and timeouts.

## v0.1.2 - 2026-11-06

- Add an embedded Scalar interface at `/docs` for model discovery and buffered
  or streaming chat requests, including cancellation. UI assets load locally
  without a CDN, hosted request proxy, or API key.
- Serve the generated OpenAPI document at `/openapi.json`, with supported
  endpoints, request constraints, error responses, and examples for both harnesses.
- Display a terminal startup panel with version and build identity, API and
  documentation URLs, working directory, and request limits.
- Show enabled harnesses in a responsive table with installed CLI versions,
  configured default models, and resolved executable paths. Version probes run
  in parallel with bounded output and timeouts; failures allow startup.
- Add `--log-format json` and installed CLI versions in structured startup logs.
- Preserve cancellation during Copilot session cleanup so a disconnected request
  does not produce a misleading inference-failure log.
- Add browser acceptance tests for streaming, cancellation, process cleanup,
  capacity recovery, errors, custom ports, and mobile rendering. CI also validates
  the OpenAPI document and generated API types. Browser CI uses the pinned
  headless shell with bounded installation and test durations.

## v0.1.1

- Align repository, module, installation commands, and executable naming.
- Use double dashes for long CLI options and single-letter short aliases.

## v0.1.0

- Initial local OpenAI-compatible gateway for authenticated Codex and Copilot
  CLIs, with model discovery, buffered and streaming chat, reasoning effort,
  cancellation, and Spring AI integration.
