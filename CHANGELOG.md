# Changelog

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
  the OpenAPI document and generated API types.

## v0.1.1

- Align repository, module, installation commands, and executable naming.
- Use double dashes for long CLI options and single-letter short aliases.

## v0.1.0

- Initial local OpenAI-compatible gateway for authenticated Codex and Copilot
  CLIs, with model discovery, buffered and streaming chat, reasoning effort,
  cancellation, and Spring AI integration.
