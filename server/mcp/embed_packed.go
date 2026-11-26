//go:build packed

package mcp

import (
	"embed"

	"github.com/thejdubb02/clrd/internal/gzfs"
)

// packed/tools.json.gz is written by go run ./server/cmd/pack (#690), and
// decoded per tools/list call, so nothing holds it decoded.
//
//go:embed packed
var packed embed.FS

var toolsFS = gzfs.New(packed, "packed", nil)
