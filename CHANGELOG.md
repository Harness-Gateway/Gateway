# Changelog

## v0.1.4 - 2026-11-06

- Stop SSE output after a transport failure without renewing the write deadline for an error frame.
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
