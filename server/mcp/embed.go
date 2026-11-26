//go:build !packed

package mcp

import "embed"

// toolsFS holds tools.json, the tools/list answer, kept as JSON so it costs
// no init code (#683). Edit tools.json by hand; TestInitializeAndList checks
// the names. The release build (-tags packed) stores it gzipped:
// embed_packed.go.
//
//go:embed tools.json
var toolsFS embed.FS
