//go:build packed

package web

import (
	"embed"
	"io/fs"

	"github.com/thejdubb02/clrd/internal/gzfs"
)

// packed holds static/ and templates/ gzipped by go run ./server/cmd/pack
// (#690, #929). Handler gives the memory used to decode templates back after
// parsing them.
//
//go:embed packed
var packed embed.FS

var files fs.FS = gzfs.New(packed, "packed", nil)

const packedBuild = true
