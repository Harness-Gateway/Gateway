# AGENTS.md

## Project Overview

Harness-Gateway is an open-source Go project that exposes AI coding harness CLIs through an OpenAI-compatible HTTP API.

Initially, the project targets GitHub Copilot CLI and OpenAI Codex CLI, with an extensible architecture for additional harnesses.

## Technology Stack

- **Language:** Go 1.25+
- **HTTP server:** Go standard library (`net/http`)
- **API contract:** Official OpenAI OpenAPI specification
- **API code generation:** `oapi-codegen` where practical
- **Testing:** Go standard library (`testing`, `httptest`)

Prefer standard libraries, established standards, and well-maintained dependencies over custom implementations.

## Architecture Guidelines

- Follow Go conventions and idiomatic practices.
- Keep the implementation simple, modular, and maintainable.
- Separate HTTP API handling from harness-specific logic.
- Use provider-independent interfaces for harness backends.
- Keep SDKs and communication protocols interchangeable.
- Prefer official SDKs and documented protocols over reverse-engineered interfaces.
- Preserve OpenAI API compatibility and avoid unnecessary proprietary extensions.
- Support context propagation, cancellation, and proper resource cleanup.
- Avoid premature abstractions and unnecessary dependencies.

## Development Guidelines

- Follow existing project conventions and structure.
- Prefer generated code over manually duplicating API schemas.
- Do not modify generated code manually.
- Write tests for new functionality and bug fixes.
- Handle errors explicitly; avoid silently ignoring failures.
- Keep changes focused and avoid unrelated refactoring.
- Document unsupported API features rather than silently ignoring them.

## Validation

Before completing changes, run:

```bash
go fmt ./...
go vet ./...
go test ./...
```

Ensure the project builds successfully and existing tests continue to pass.
