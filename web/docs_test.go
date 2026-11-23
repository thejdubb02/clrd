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
// top-level guide is linked from the README's Docs list, an integration
// guide from the integrations index, and every page from the served /docs/
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
	integrations, err := fs.ReadFile(clrd.Files, "docs/integrations/README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, index := get(t, Handler(NewFake(), ""), "/docs/")
	n := 0
	err = fs.WalkDir(clrd.Files, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" {
			return err
		}
		n++
		if dir, file := path.Split(p); dir == "docs/" {
			if !strings.Contains(docsList, "]("+p+")") {
				t.Errorf("README ## Docs does not link %s", p)
			}
		} else if dir == "docs/integrations/" && file != "README.md" && !strings.Contains(string(integrations), "]("+file+")") {
			t.Errorf("docs/integrations/README.md does not link %s", file)
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
