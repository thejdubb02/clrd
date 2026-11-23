//go:build packed

package server

import (
	"embed"

	"github.com/thejdubb02/clrd/internal/gzfs"
)

// packed/openapi.json.gz is written by go run ./server/cmd/pack (#690).
//
//go:embed packed
var packed embed.FS

var apiDoc = gzfs.New(packed, "packed", nil)

const packedBuild = true
