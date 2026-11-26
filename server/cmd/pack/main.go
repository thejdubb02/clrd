// Command pack gzips the text files the release binary embeds, for the
// packed build (#690). From the repo root, before go build -tags packed:
//
//	go run ./server/cmd/pack
//
// It wipes its output folders first, so a deleted source never ships. The
// output is gitignored; a plain go build does not need it.
package main

import (
	"compress/gzip"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/thejdubb02/clrd/internal/changelog"
)

// Each source (a folder or a file) is stored under out as name.gz.
var packs = []struct{ src, out string }{
	{"docs", "packed"},
	{"web/static", "web/packed"},
	{"web/templates", "web/packed"},
	{"server/openapi.json", "server/packed"},
	{"server/mcp/tools.json", "server/mcp/packed"},
}

func main() {
	for _, p := range packs {
		if err := os.RemoveAll(p.out); err != nil {
			log.Fatal(err)
		}
	}
	for _, p := range packs {
		base := filepath.Dir(p.src)
		err := filepath.WalkDir(p.src, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if n := d.Name(); path != p.src && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_")) {
				if d.IsDir() { // go:embed leaves these out too
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(base, path)
			return pack(path, filepath.Join(p.out, rel)+".gz")
		})
		if err != nil {
			log.Fatal(err)
		}
	}
	md, _, err := changelog.Assemble(os.DirFS("."))
	if err != nil {
		log.Fatal(err)
	}
	if err := packBytes([]byte(md), "packed/CHANGELOG.md.gz"); err != nil {
		log.Fatal(err)
	}
}

func pack(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return packBytes(b, dst)
}

func packBytes(b []byte, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	z, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	if _, err := z.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := z.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
