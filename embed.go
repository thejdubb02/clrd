//go:build !packed

// Package clrd holds the files shared by every surface: the docs, the brand
// assets and the changelog (version.go). They live at the repo root, and
// go:embed cannot reach above a package directory, so they are embedded here.
// The release build (-tags packed) stores docs gzipped: embed_packed.go.
package clrd

import (
	"embed"
	"io/fs"

	"github.com/thejdubb02/clrd/internal/changelog"
)

//go:embed docs assets/*.* assets/fonts CHANGELOG.md changelog.d
var files embed.FS

// Files reads the same in both builds.
var Files fs.FS = files

func readChangelog() string {
	md, _, err := changelog.Assemble(Files)
	if err != nil {
		panic(err)
	}
	return md
}
