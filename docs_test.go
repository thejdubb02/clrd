package clrd

import (
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
)

// The README Docs list names every guide in docs/ and the integrations
// index, so a new guide cannot go missing from the repo page (#592). The
// site's /docs/ index lists every page on its own.
func TestReadmeListsDocs(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, list, _ := strings.Cut(string(b), "\n## Docs\n")
	list, _, _ = strings.Cut(list, "\n## ")
	pages, _ := fs.Glob(Files, "docs/*.md")
	for _, p := range append(pages, "docs/integrations/README.md") {
		if !strings.Contains(list, "]("+p+")") {
			t.Errorf("README.md ## Docs does not link %s", p)
		}
	}
	// And the integrations index links every integration guide.
	idx, err := fs.ReadFile(Files, "docs/integrations/README.md")
	if err != nil {
		t.Fatal(err)
	}
	guides, _ := fs.Glob(Files, "docs/integrations/*.md")
	for _, p := range guides {
		if b := path.Base(p); b != "README.md" && !strings.Contains(string(idx), "("+b+")") {
			t.Errorf("docs/integrations/README.md does not link %s", b)
		}
	}
}
