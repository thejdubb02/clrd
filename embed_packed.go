//go:build packed

package clrd

import (
	"embed"
	"io/fs"

	"github.com/thejdubb02/clrd/internal/gzfs"
)

// packed/docs is docs/ gzipped by go run ./server/cmd/pack (#690). Assets
// are already compressed, and CHANGELOG.md is read once at start.
//
//go:embed packed
var packed embed.FS

//go:embed assets CHANGELOG.md
var raw embed.FS

var Files fs.FS = gzfs.New(packed, "packed", raw)
