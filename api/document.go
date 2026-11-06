package api

import _ "embed"

// OpenAPI is the generated specification served by the gateway and Scalar.
//
//go:embed openapi.json
var OpenAPI []byte
