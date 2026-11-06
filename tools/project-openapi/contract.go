package main

import "github.com/harness-gateway/harness-gateway/internal/buildinfo"

const sseExample = `data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":0,"model":"codex","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello!"},"finish_reason":null}]}

data: {"id":"chatcmpl-example","object":"chat.completion.chunk","created":0,"model":"codex","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

// gatewayDocument adds the HTTP operations and gateway-specific constraints to
// the normalized types projected from the pinned upstream specification.
func gatewayDocument(schemas node) node {
	request := properties(schemas["ChatRequest"].(node))
	request["model"].(node)["description"] = "Use codex or copilot for the CLI's configured default, or an exact ID from GET /v1/models such as `codex/<native-model-id>`."
	request["model"].(node)["example"] = "codex"
	request["n"].(node)["maximum"] = 1
	request["n"].(node)["description"] = "Only one completion is supported; omit this field or use 1."
	request["reasoning_effort"] = node{"type": "string", "x-go-type": "string", "enum": []any{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, "description": "Optional reasoning effort forwarded to the CLI. Supported levels depend on the selected native model; omission preserves its default."}
	request["stream"].(node)["description"] = "When true, return incremental server-sent events instead of a buffered JSON response."
	schemas["StreamOptions"].(node)["description"] = "Only valid with stream=true."
	obfuscation := properties(schemas["StreamOptions"].(node))["include_obfuscation"].(node)
	obfuscation["enum"], obfuscation["x-go-type"] = []any{false}, "bool"
	message := properties(schemas["ChatMessage"].(node))
	message["content"] = node{
		"x-go-type": "json.RawMessage",
		"oneOf": []any{
			node{"type": "string"},
			node{"type": "array", "minItems": 1, "items": ref("TextPart")},
		},
		"description": "Plain text or a nonempty array of text parts. Images, audio, tool calls, and extra content-part fields are unsupported.",
	}
	schemas["TextPart"] = node{"type": "object", "additionalProperties": false, "required": []any{"type", "text"}, "properties": node{
		"type": node{"type": "string", "enum": []any{"text"}, "x-go-type": "string"}, "text": node{"type": "string"},
	}}
	schemas["Health"] = node{"type": "object", "required": []any{"status"}, "properties": node{"status": node{"type": "string", "enum": []any{"ok"}, "x-go-type": "string"}}}

	examples := node{}
	for _, provider := range []string{"codex", "copilot"} {
		for _, stream := range []bool{false, true} {
			body := node{"model": provider, "messages": []any{node{"role": "system", "content": "Be brief"}, node{"role": "user", "content": "Say hello"}}}
			name, summary := provider, provider+" (buffered)"
			if stream {
				body["stream"] = true
				body["stream_options"] = node{"include_usage": true}
				name, summary = provider+"Stream", provider+" (streaming)"
			}
			examples[name] = node{"summary": summary, "value": body}
		}
	}
	chatResponses := errorResponses("400", "404", "405", "413", "415", "429", "502", "503", "504")
	chatResponses["200"] = node{
		"description": "A buffered completion, or an SSE stream when stream=true. Each SSE data field contains a Chunk JSON object, followed by data: [DONE] on success. An error after streaming starts emits an ErrorResponse and closes without [DONE]. Cancel the browser request to stop its CLI operation.",
		"headers":     node{"X-Request-ID": node{"description": "Request identifier also recorded in the gateway log.", "schema": node{"type": "string"}}},
		"content": node{
			"application/json": node{"schema": ref("Completion"), "example": node{
				"id": "chatcmpl-example", "object": "chat.completion", "created": 0, "model": "codex",
				"choices": []any{node{"index": 0, "message": node{"role": "assistant", "content": "Hello!"}, "finish_reason": "stop"}},
			}},
			"text/event-stream": node{"schema": node{"type": "string"}, "example": sseExample},
		},
	}
	modelResponses := errorResponses("405", "429", "502", "503", "504")
	modelResponses["200"] = node{"description": "Configured aliases and currently available native model IDs. Discovery uses the local authenticated CLIs.", "content": node{"application/json": node{"schema": ref("ModelList"), "example": node{"object": "list", "data": []any{
		node{"id": "codex", "object": "model", "owned_by": "codex", "created": 0},
		node{"id": "copilot", "object": "model", "owned_by": "copilot", "created": 0},
	}}}}}
	healthResponses := errorResponses("405")
	healthResponses["200"] = node{"description": "The gateway is running.", "content": node{"application/json": node{"schema": ref("Health"), "example": node{"status": "ok"}}}}
	return node{
		"openapi": "3.0.3",
		"info": node{
			"title": "Harness-Gateway", "version": buildinfo.Version,
			"description":       "Test your local Codex and Copilot CLIs through the OpenAI-compatible API. Start with model discovery, then send a buffered or streaming completion. No API key is required: authentication stays with the local CLIs.\n\nSystem/developer messages must precede conversation messages; the last message must be from the user. Only text chat is supported. Tools, images, audio, sampling parameters, and structured output are rejected. Requests are limited to 1 MiB and assistant text to 16 MiB.",
			"x-upstream-source": sourceURL, "x-upstream-sha256": checksum,
			"license": node{"name": "Apache-2.0", "url": "https://www.apache.org/licenses/LICENSE-2.0"},
		},
		"servers": []any{node{"url": "/", "description": "This gateway (current host and port)"}},
		"tags":    []any{node{"name": "Gateway"}, node{"name": "Models"}, node{"name": "Chat"}},
		"paths": node{
			"/health": node{"get": node{
				"operationId": "getHealth", "summary": "Check gateway health", "tags": []any{"Gateway"},
				"description": "Checks gateway liveness. This does not start a CLI or verify upstream authentication.",
				"responses":   healthResponses,
			}},
			"/v1/models": node{"get": node{"operationId": "listModels", "summary": "Discover available models", "tags": []any{"Models"}, "responses": modelResponses}},
			"/v1/chat/completions": node{"post": node{
				"operationId": "createChatCompletion", "summary": "Create a chat completion", "tags": []any{"Chat"},
				"description": "Send text to a local CLI. Choose an example for codex or copilot; set stream=true for incremental output. Model aliases use CLI defaults. Native model IDs come from GET /v1/models. Requests share the configured concurrency limit with model discovery.",
				"requestBody": node{"required": true, "content": node{"application/json": node{"schema": ref("ChatRequest"), "examples": examples}}},
				"responses":   chatResponses,
			}},
		},
		"components": node{"schemas": schemas},
	}
}

func errorResponses(codes ...string) node {
	descriptions := map[string]string{
		"400": "Invalid or unsupported request.", "404": "Unknown model or endpoint.", "405": "HTTP method is not supported for this endpoint.", "413": "Request exceeds 1 MiB.",
		"415": "Content-Type must be application/json.", "429": "Gateway capacity is full; retry after an active operation finishes.",
		"502": "The local CLI or model discovery failed; check the gateway log.", "503": "The gateway is shutting down.", "504": "The operation timed out.",
	}
	responses := node{}
	for _, code := range codes {
		responses[code] = node{"description": descriptions[code], "content": node{"application/json": node{"schema": ref("ErrorResponse")}}}
		if code == "405" {
			responses[code].(node)["headers"] = node{"Allow": node{"description": "Supported HTTP methods for this endpoint.", "schema": node{"type": "string"}}}
		}
	}
	return responses
}
