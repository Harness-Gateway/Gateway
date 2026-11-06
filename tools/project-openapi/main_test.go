package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/harness-gateway/harness-gateway/api"
)

func TestPublishedGatewayContractIsCurrent(t *testing.T) {
	var document struct {
		Components struct{ Schemas node }
	}
	if err := json.Unmarshal(api.OpenAPI, &document); err != nil {
		t.Fatal(err)
	}
	updated, err := json.MarshalIndent(gatewayDocument(document.Components.Schemas), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(updated, '\n'), api.OpenAPI) {
		t.Fatal("gateway documentation is stale; reproduce the OpenAPI projection and run go generate ./api")
	}
}

func TestChecksumRequired(t *testing.T) {
	if _, err := project([]byte(`{"components":{"schemas":{}}}`)); err == nil {
		t.Fatal("accepted an unpinned schema")
	}
}

func TestNullableNormalization(t *testing.T) {
	input := node{"anyOf": []any{node{"type": "string", "enum": []any{"stop", nil}}, node{"type": "null"}}, "description": "upstream", "examples": []any{"stop"}}
	want := node{"type": "string", "enum": []any{"stop"}, "nullable": true, "x-go-type": "string"}
	if actual := normalize(input); !reflect.DeepEqual(actual, want) {
		t.Fatalf("got %#v, want %#v", actual, want)
	}
	if actual := normalize(node{"type": []any{"integer", "null"}, "format": "unixtime"}); !reflect.DeepEqual(actual, node{"type": "integer", "nullable": true, "format": "int64"}) {
		t.Fatal(actual)
	}
}

func TestProjectionDoesNotMutateUpstream(t *testing.T) {
	original := node{"type": "object", "properties": node{"first": node{"type": "string"}, "second": node{"type": "boolean"}}, "required": []any{"first", "second"}}
	p := projector{upstream: map[string]node{"Base": original}}
	combined := node{"allOf": []any{ref("Base"), node{"type": "object", "properties": node{"third": node{"type": "integer"}}}}}
	selected := p.selectFields(combined, "first", "third")
	if !reflect.DeepEqual(selected["required"], []any{"first"}) || len(properties(selected)) != 2 {
		t.Fatal(selected)
	}
	properties(selected)["first"].(node)["type"] = "integer"
	if properties(original)["first"].(node)["type"] != "string" {
		t.Fatal("projection mutated upstream")
	}
}
