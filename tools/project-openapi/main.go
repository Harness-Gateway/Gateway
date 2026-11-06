// Command project-openapi projects the MVP wire types from a pinned official
// OpenAI schema. It has no third-party dependencies and performs no downloads.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

const commit = "85a23866c06a29098083ebb11d5ec70438305def"
const sourceURL = "https://raw.githubusercontent.com/openai/openai-openapi/" + commit + "/openapi.json"
const checksum = "0e4dc3d7f52cc7d7c41940587b5ae23c0e3173bd4b4384f9d673ee90185ebafb"

type node = map[string]any

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("project-openapi", flag.ContinueOnError)
	input := flags.String("input", "", "local official OpenAPI JSON file (pinned checksum required)")
	output := flags.String("output", "api/openapi.json", "output projection")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || flags.NArg() != 0 {
		return errors.New("usage: go run ./tools/project-openapi -input /path/to/openapi.json [-output api/openapi.json]")
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		return err
	}
	projected, err := project(data)
	if err != nil {
		return err
	}
	return os.WriteFile(*output, projected, 0644)
}

func project(data []byte) ([]byte, error) {
	if fmt.Sprintf("%x", sha256.Sum256(data)) != checksum {
		return nil, errors.New("upstream schema checksum mismatch")
	}
	var document struct {
		Components struct {
			Schemas map[string]node `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	p := projector{upstream: document.Components.Schemas}
	schemas := map[string]node{}
	request := p.selectFields(p.upstream["CreateChatCompletionRequest"], "model", "messages", "stream", "stream_options", "n", "reasoning_effort")
	schemas["ChatRequest"] = request
	properties(request)["model"] = node{"type": "string"}
	properties(request)["messages"].(node)["items"] = ref("ChatMessage")
	properties(request)["stream_options"] = ref("StreamOptions")
	properties(request)["reasoning_effort"] = node{"type": "string"}
	message := p.selectFields(p.upstream["ChatCompletionRequestUserMessage"], "role", "content")
	schemas["ChatMessage"] = message
	properties(message)["role"] = node{"type": "string", "enum": []any{"system", "developer", "user", "assistant"}}
	content := properties(message)["content"].(node)
	content["x-go-type"] = "json.RawMessage"
	// The inbound adapter validates strings and arrays of text parts explicitly.
	delete(content, "oneOf")
	delete(content, "anyOf")
	schemas["StreamOptions"] = p.selectFields(p.upstream["ChatCompletionStreamOptions"]["anyOf"].([]any)[0].(node), "include_usage", "include_obfuscation")
	for _, selection := range []struct {
		name, original string
		fields         []string
	}{
		{"Completion", "CreateChatCompletionResponse", []string{"id", "object", "created", "model", "choices", "usage"}},
		{"Chunk", "CreateChatCompletionStreamResponse", []string{"id", "object", "created", "model", "choices", "usage"}},
		{"ResponseMessage", "ChatCompletionResponseMessage", []string{"role", "content"}},
		{"Delta", "ChatCompletionStreamResponseDelta", []string{"role", "content"}},
		{"Usage", "CompletionUsage", []string{"prompt_tokens", "completion_tokens", "total_tokens"}},
		{"Model", "Model", []string{"id", "object", "created", "owned_by"}},
		{"ModelList", "ListModelsResponse", []string{"object", "data"}},
		{"APIError", "Error", []string{"message", "type", "param", "code"}},
		{"ErrorResponse", "ErrorResponse", []string{"error"}},
	} {
		schemas[selection.name] = p.selectFields(p.upstream[selection.original], selection.fields...)
	}
	for _, selection := range []struct {
		owner, name string
		fields      []string
	}{
		{"Completion", "CompletionChoice", []string{"index", "message", "finish_reason"}},
		{"Chunk", "ChunkChoice", []string{"index", "delta", "finish_reason"}},
	} {
		choices := properties(schemas[selection.owner])["choices"].(node)
		schemas[selection.name] = p.selectFields(choices["items"].(node), selection.fields...)
		choices["items"] = ref(selection.name)
		properties(schemas[selection.owner])["usage"] = ref("Usage")
	}
	properties(schemas["CompletionChoice"])["message"] = ref("ResponseMessage")
	properties(schemas["ChunkChoice"])["delta"] = ref("Delta")
	properties(schemas["ModelList"])["data"].(node)["items"] = ref("Model")
	properties(schemas["ErrorResponse"])["error"] = ref("APIError")
	spec := gatewayDocument(normalizeSchemas(schemas))
	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// Schema layout assertions below are invariants of the checksum-pinned input.
// Clone before projecting so narrowing one type cannot change another type.
type projector struct{ upstream map[string]node }

func (p projector) flatten(schema node) node {
	if reference, ok := schema["$ref"].(string); ok {
		return p.flatten(p.upstream[reference[strings.LastIndex(reference, "/")+1:]])
	}
	if parts, ok := schema["allOf"].([]any); ok {
		props, required := node{}, []any{}
		for _, part := range parts {
			flat := p.flatten(part.(node))
			if fields, ok := flat["properties"].(node); ok {
				for key, value := range fields {
					props[key] = value
				}
			}
			if fields, ok := flat["required"].([]any); ok {
				required = append(required, fields...)
			}
		}
		return node{"type": "object", "properties": props, "required": required}
	}
	return clone(schema).(node)
}

func (p projector) selectFields(schema node, fields ...string) node {
	flat := p.flatten(schema)
	props, required := node{}, []any{}
	for _, field := range fields {
		props[field] = properties(flat)[field]
	}
	if all, ok := flat["required"].([]any); ok {
		for _, field := range all {
			if _, exists := props[field.(string)]; exists {
				required = append(required, field)
			}
		}
	}
	return node{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func properties(schema node) node { return schema["properties"].(node) }
func ref(name string) node        { return node{"$ref": "#/components/schemas/" + name} }

func clone(value any) any {
	switch value := value.(type) {
	case node:
		copy := node{}
		for key, field := range value {
			copy[key] = clone(field)
		}
		return copy
	case []any:
		copy := make([]any, len(value))
		for i, field := range value {
			copy[i] = clone(field)
		}
		return copy
	default:
		return value
	}
}

func normalizeSchemas(schemas map[string]node) node {
	result := node{}
	for key, schema := range schemas {
		result[key] = normalize(schema)
	}
	return result
}

// Convert the selected OpenAPI 3.1 nullable forms to the 3.0 forms accepted by
// the pinned code generator. Remove upstream examples and documentation metadata.
func normalize(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, field := range value {
			result[i] = normalize(field)
		}
		return result
	case node:
		result := node{}
		for key, field := range value {
			switch key {
			case "x-oaiMeta", "examples", "description", "x-stainless-const":
				continue
			}
			result[key] = normalize(field)
		}
		if types, ok := result["type"].([]any); ok {
			for _, kind := range types {
				if kind != "null" {
					result["type"] = kind
					break
				}
			}
			result["nullable"] = true
		}
		if variants, ok := result["anyOf"].([]any); ok && len(variants) == 2 {
			var selected node
			for i, variant := range variants {
				if variant.(node)["type"] == "null" {
					selected = variants[1-i].(node)
				}
			}
			if selected != nil {
				delete(result, "anyOf")
				for key, field := range selected {
					if _, exists := result[key]; !exists {
						result[key] = field
					}
				}
				result["nullable"] = true
			}
		}
		if values, ok := result["enum"].([]any); ok {
			filtered := []any{}
			for _, field := range values {
				if field != nil {
					filtered = append(filtered, field)
				}
			}
			result["enum"], result["x-go-type"] = filtered, "string"
		}
		if result["format"] == "unixtime" {
			result["format"] = "int64"
		}
		return result
	default:
		return value
	}
}
