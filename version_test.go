package clrd

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The binary carries the recent history only (#580): past the limit, cut a
// version so the script moves the oldest ones to CHANGELOG-archive.md.
func TestChangelogSize(t *testing.T) {
	if len(Changelog) > ChangelogLimit {
		t.Fatalf("CHANGELOG.md is %d bytes, over %d: the fix is cutting a version: go run ./server/cmd/cutversion [-summary-file F] <next version> (SPEC section 5.2)", len(Changelog), ChangelogLimit)
	}
}

// Every top-level item of the real file comes out once, in some section.
func TestParseChangelogReal(t *testing.T) {
	want := 0
	_, body, _ := strings.Cut(Changelog, "\n## ")
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "- ") {
			want++
		}
	}
	got := 0
	for _, r := range ParseChangelog(Changelog) {
		for _, c := range r.Changes {
			got += len(c.Items)
			for _, it := range c.Items {
				if strings.Contains(it, "\n") || strings.Contains(it, "  ") {
					t.Fatalf("item not joined: %q", it)
				}
			}
		}
	}
	if want == 0 || got != want {
		t.Fatalf("parsed %d items, file has %d", got, want)
	}
}

func TestParseChangelog(t *testing.T) {
	md := "# Changelog\n\n- not an item\n\n## Unreleased\n\n### Added\n\n## 0.2.0 - 2026-12-01\n\n### Added\n\n- One,\n  wrapped.\n- Two\n\n### Fixed\n\n- Three\n\n### Added\n\n- Four\n\n## 0.1.0 - 2026-11-23\n\n### Removed\n\n- Five\n\n### Security\n"
	got := ParseChangelog(md)
	want := []Release{
		{"0.2.0", "2026-12-01", []Change{{"Added", []string{"One, wrapped.", "Two", "Four"}}, {"Fixed", []string{"Three"}}}},
		{"0.1.0", "2026-11-23", []Change{{"Removed", []string{"Five"}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// The exact keys the web, Android and desktop apps read (SPEC section 5.2),
// with no null arrays.
func TestVersionJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(VersionJSON(), &v); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for k := range v {
		keys = append(keys, k)
	}
	if len(keys) != 5 || v["version"] == nil || v["commit"] == nil || v["date"] == nil || v["full_history"] != FullHistory {
		t.Fatalf("keys: %v", v)
	}
	h := v["history"].([]any)
	r := h[0].(map[string]any)
	c := r["changes"].([]any)[0].(map[string]any)
	if len(r) != 3 || r["version"] == nil || r["date"] == nil || len(c) != 2 || c["section"] == nil || c["items"] == nil {
		t.Fatalf("release: %v", r)
	}
	if strings.Contains(string(VersionJSON()), ":null") {
		t.Fatal("a null in the version JSON")
	}
}
