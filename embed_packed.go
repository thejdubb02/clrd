//go:build packed

package clrd

import (
	"embed"
	"io/fs"

	"github.com/thejdubb02/clrd/internal/gzfs"
)

// Docs and the assembled changelog are gzipped by server/cmd/pack (#690).
// Assets are already compressed.
//
//go:embed packed
var packed embed.FS

//go:embed assets
var raw embed.FS

var Files fs.FS = gzfs.New(packed, "packed", raw)

func readChangelog() string {
	md, err := fs.ReadFile(Files, "CHANGELOG.md")
	if err != nil {
		panic(err)
	}
	return string(md)
}
