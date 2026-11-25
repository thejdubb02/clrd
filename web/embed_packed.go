//go:build packed

package web

import (
	"embed"
	"io/fs"

	"github.com/thejdubb02/clrd/internal/gzfs"
)

// packed/static is static/ gzipped by go run ./server/cmd/pack (#690).
// Templates stay raw: they are parsed at start, so packing them costs memory.
//
//go:embed packed
var packed embed.FS

//go:embed templates
var raw embed.FS

var files fs.FS = gzfs.New(packed, "packed", raw)

const packedBuild = true
