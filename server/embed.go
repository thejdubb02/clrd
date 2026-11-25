//go:build !packed

package server

import "embed"

// apiDoc holds openapi.json; the release build (-tags packed) stores it
// gzipped: embed_packed.go.
//
//go:embed openapi.json
var apiDoc embed.FS

const packedBuild = false
