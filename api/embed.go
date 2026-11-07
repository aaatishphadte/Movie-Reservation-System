// Package api embeds the OpenAPI specification and the Swagger UI page, so the docs ship
// inside the binary and can never drift from a separately deployed copy.
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPISpec []byte

//go:embed docs.html
var DocsPage []byte
