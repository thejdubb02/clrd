// Package clrd holds the files shared by every surface: the docs and the brand
// assets. They live at the repo root, and go:embed cannot reach above a
// package directory, so they are embedded here and imported by web.
package clrd

import "embed"

//go:embed docs assets
var Files embed.FS
