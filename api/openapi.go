// Package openapi embeds the OpenAPI 3.0 spec (openapi.yaml, in this same
// directory) into the compiled binary so it can be served without shipping
// any extra files alongside the distroless image.
package openapi

import _ "embed"

//go:embed openapi.yaml
var Spec []byte
