//go:build !packed

package server

import "embed"

// serverFiles holds the OpenAPI document and emoji aliases; the release build (-tags packed) stores it
// gzipped: embed_packed.go.
//
//go:embed openapi.json emoji_aliases.txt
var serverFiles embed.FS

const packedBuild = false
