package web

import (
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/thejdubb02/clrd"
)

// Every docs page is reachable from the repo page and the site (#592): a
// top-level guide or folder index is linked from the README's Docs list, a
// guide in a folder from that folder's README, and every page from the served /docs/
// index, so a new guide cannot go missing from either.
func TestDocsLinked(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, docsList, ok := strings.Cut(string(readme), "\n## Docs\n")
	if !ok {
		t.Fatal("README has no ## Docs section")
	}
	docsList, _, _ = strings.Cut(docsList, "\n## ")
	code, index := get(t, Handler(NewFake(), ""), "/docs/")
	if code != 200 {
		t.Fatalf("/docs/: %d", code)
	}
	n := 0
	err = fs.WalkDir(clrd.Files, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" {
			return err
		}
		n++
		// A page in a subfolder is linked from that folder's README, and the
		// README itself (like any top-level page) from the repo README.
		if dir, file := path.Split(p); dir == "docs/" || file == "README.md" {
			if !strings.Contains(docsList, "]("+p+")") {
				t.Errorf("README ## Docs does not link %s", p)
			}
		} else if idx, err := fs.ReadFile(clrd.Files, dir+"README.md"); err != nil || !strings.Contains(string(idx), "]("+file+")") {
			t.Errorf("%sREADME.md does not link %s", dir, file)
		}
		if key := docKey(strings.TrimSuffix(strings.TrimPrefix(p, "docs/"), ".md")); !strings.Contains(index, `href="/docs/`+key+`"`) {
			t.Errorf("/docs/ index does not link %s", p)
		}
		return nil
	})
	if err != nil || n < 10 {
		t.Fatalf("walked %d docs: %v", n, err)
	}
}
