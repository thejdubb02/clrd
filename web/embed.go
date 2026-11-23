//go:build !packed

package web

import (
	"embed"
	"io/fs"
)

// The release build (-tags packed) stores static gzipped: embed_packed.go.
//
//go:embed templates static
var embedded embed.FS

var files fs.FS = embedded

const packedBuild = false
