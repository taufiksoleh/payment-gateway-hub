// Package apidocs embeds the OpenAPI spec and its HTML viewer so the hub can
// serve them at /openapi.yaml and /docs.
package apidocs

import _ "embed"

//go:embed openapi.yaml
var Spec []byte

//go:embed index.html
var Page []byte
