// Package clrd holds the files shared by every surface: the docs, the brand
// assets and the changelog (version.go). They live at the repo root, and
// go:embed cannot reach above a package directory, so they are embedded here.
package clrd

import "embed"

//go:embed docs assets CHANGELOG.md
var Files embed.FS
