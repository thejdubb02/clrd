//go:build packed

package server

import (
	"embed"

	"github.com/thejdubb02/clrd/internal/gzfs"
)

// go run ./server/cmd/pack writes the compressed server files (#690).
//
//go:embed packed
var packed embed.FS

var serverFiles = gzfs.New(packed, "packed", nil)

const packedBuild = true
