package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/thejdubb02/clrd"
)

func items(md string) (n int) {
	for _, r := range clrd.ParseChangelog(md) {
		for _, c := range r.Changes {
			n += len(c.Items)
		}
	}
	return n
}

// The real file cuts cleanly: every entry lands in the new version and
// Unreleased stays, empty, for the next one.
func TestCutReal(t *testing.T) {
	// Seeded, so this still runs right after a real cut empties Unreleased.
	md := strings.Replace(clrd.Changelog, "\n## Unreleased\n", "\n## Unreleased\n\n### Added\n\n- seed\n", 1)
	out, arch, err := cut(md, "", "9.9.9", "2026-11-23", "")
	if err != nil {
		t.Fatal(err)
	}
	rs := clrd.ParseChangelog(out)
	if rs[0].Version != "9.9.9" || rs[0].Date != "2026-11-23" || items(out)+items(arch) != items(md) {
		t.Fatalf("cut: %v %d", rs[0].Version, items(out))
	}
	if !strings.Contains(out, "\n## Unreleased\n\n## 9.9.9 - 2026-11-23\n") || !strings.HasPrefix(out, strings.SplitN(clrd.Changelog, "\n## ", 2)[0]) {
		t.Fatal("Unreleased heading or preamble lost")
	}
	if _, _, err := cut(out, "", "9.9.10", "2026-11-24", ""); err == nil || !strings.Contains(err.Error(), "nothing") {
		t.Fatalf("empty cut: %v", err)
	}
	for _, bad := range [][2]string{{"9.9.9", "2026-11-24"}, {"v1", "2026-11-24"}, {"1.0.0", "today"}} {
		if _, _, err := cut(out+"- x\n", "", bad[0], bad[1], ""); err == nil {
			t.Fatalf("%v: no error", bad)
		}
	}
}

// With a summary, the summary stays and the full entries go to the archive
// under the same heading.
func TestCutSummary(t *testing.T) {
	md := strings.Replace(clrd.Changelog, "\n## Unreleased\n", "\n## Unreleased\n\n### Added\n\n- seed\n", 1)
	out, arch, err := cut(md, archiveHead, "9.9.9", "2026-11-23", "### Added\n\n- Short summary.\n")
	if err != nil {
		t.Fatal(err)
	}
	kept, moved := clrd.ParseChangelog(out), clrd.ParseChangelog(arch)
	if kept[0].Version != "9.9.9" || len(kept[0].Changes) != 1 || len(kept[0].Changes[0].Items) != 1 {
		t.Fatalf("changelog: %+v", kept)
	}
	if moved[0].Version != "9.9.9" || moved[0].Date != "2026-11-23" || items(out)-1+items(arch) != items(md) || !strings.HasPrefix(arch, archiveHead) {
		t.Fatalf("archive: %d items", items(arch))
	}
	if _, _, err := cut(md, "", "9.9.9", "2026-11-23", "just prose"); err == nil {
		t.Fatal("summary with no items: no error")
	}
}

// Near the limit the oldest versions move to the archive, newest first,
// with nothing lost, and the embedded file stays under the limit.
func TestCutArchives(t *testing.T) {
	entry := "- " + strings.Repeat("word ", 1700) + "\n" // about 8.5 KB
	md := "# Changelog\n\n## Unreleased\n\n### Added\n\n" + entry
	for v := 12; v >= 1; v-- {
		md += fmt.Sprintf("\n## 0.%d.0 - 2026-01-%02d\n\n### Fixed\n\n%s", v, v, entry)
	}
	arch := archiveHead + "\n## 0.0.1 - 2025-01-01\n\n### Added\n\n- First\n"
	out, newArch, err := cut(md, arch, "1.0.0", "2026-02-01", "")
	if err != nil {
		t.Fatal(err)
	}
	kept, moved := clrd.ParseChangelog(out), clrd.ParseChangelog(newArch)
	if len(out) > near || len(kept) != 10 || kept[0].Version != "1.0.0" || kept[9].Version != "0.4.0" {
		t.Fatalf("kept %d versions, %d bytes", len(kept), len(out))
	}
	if len(moved) != 4 || moved[0].Version != "0.3.0" || moved[3].Version != "0.0.1" || !strings.HasPrefix(newArch, archiveHead) {
		t.Fatalf("archive: %+v", moved)
	}
	if items(out)+items(newArch) != items(md)+items(arch) {
		t.Fatal("an entry was lost")
	}

	// Ten huge versions: the limit wins over keeping ten, never the newest.
	big := "- " + strings.Repeat("word ", 4000) + "\n"
	md = "# Changelog\n\n## Unreleased\n\n### Added\n\n" + big
	for v := 9; v >= 1; v-- {
		md += fmt.Sprintf("\n## 0.%d.0 - 2026-01-%02d\n\n### Fixed\n\n%s", v, v, big)
	}
	out, newArch, _ = cut(md, "", "1.0.0", "2026-02-01", "")
	if len(out) > clrd.ChangelogLimit || len(clrd.ParseChangelog(out)) >= 10 || items(out)+items(newArch) != items(md) {
		t.Fatalf("over the limit: %d bytes", len(out))
	}
	// One version bigger than the limit by itself is refused, not written.
	if _, _, err := cut("## Unreleased\n\n### Added\n\n"+strings.Repeat(big, 5), "", "1.0.0", "2026-02-01", ""); err == nil {
		t.Fatal("oversize version: no error")
	}
}

func TestRunConsumesFragments(t *testing.T) {
	t.Chdir(t.TempDir())
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll("changelog.d/fixed", 0o755); err != nil {
		t.Fatal(err)
	}
	write(changelog, "# Changelog\n\n## Unreleased\n\n### Added\n\n- Legacy entry.\n\n## 1.0.0 - 2026-01-01\n\n### Fixed\n\n- Older entry.\n")
	write("changelog.d/README.md", "Instructions.")
	const fragment = "changelog.d/fixed/issue.md"
	write(fragment, "- A pending fix,\n  with a continuation.\n")

	if err := run("1.0.0", "2026-01-02", ""); err == nil {
		t.Fatal("cut a duplicate version")
	}
	if _, err := os.Stat(fragment); err != nil {
		t.Fatalf("failed cut removed the fragment: %v", err)
	}
	write("summary.md", "### Fixed\n\n- Short summary.\n")
	if err := run("1.0.1", "2026-01-02", "summary.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fragment); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fragment left behind: %v", err)
	}
	if _, err := os.Stat("changelog.d/README.md"); err != nil {
		t.Fatal(err)
	}
	md, err := os.ReadFile(changelog)
	if err != nil {
		t.Fatal(err)
	}
	arch, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	kept, moved := clrd.ParseChangelog(string(md)), clrd.ParseChangelog(string(arch))
	if len(kept) != 2 || kept[0].Version != "1.0.1" || kept[0].Changes[0].Items[0] != "Short summary." {
		t.Fatalf("changelog: %+v", kept)
	}
	if len(moved) != 1 || moved[0].Version != "1.0.1" || !strings.Contains(string(arch), "- Legacy entry.") ||
		!strings.Contains(string(arch), "- A pending fix,\n  with a continuation.") {
		t.Fatalf("archive: %+v", moved)
	}
}
