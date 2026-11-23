package clrd

import (
	"encoding/json"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
)

// Commit and Built are stamped at build time (docs/self-host.md):
// -ldflags "-X github.com/thejdubb02/clrd.Commit=... -X github.com/thejdubb02/clrd.Built=..."
var Commit, Built string

// Changelog is the embedded CHANGELOG.md. It must stay under ChangelogLimit
// bytes; the cut script (server/cmd/cutversion) archives older versions.
var Changelog = func() string { b, _ := Files.ReadFile("CHANGELOG.md"); return string(b) }()

const ChangelogLimit = 96000

// FullHistory is where every version is, including the archived ones.
const FullHistory = "https://github.com/thejdubb02/clrd/blob/main/CHANGELOG.md"

// Release is one version section of the changelog (SPEC section 5.2).
type Release struct {
	Version string   `json:"version"`
	Date    string   `json:"date"`
	Changes []Change `json:"changes"`
}

// Change is one section of a release, like Added or Fixed.
type Change struct {
	Section string   `json:"section"`
	Items   []string `json:"items"`
}

// ParseChangelog reads the changelog as this repo writes it: "## <version>
// - <date>" (or "## Unreleased"), "### <section>", then "- " items whose
// wrapped lines are indented. A repeated section merges into the first, and
// an empty Unreleased is left out.
func ParseChangelog(md string) []Release {
	rs := []Release{}
	sec, inItem := -1, false
	for _, line := range strings.Split(md, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			v, d, _ := strings.Cut(h, " - ")
			rs = append(rs, Release{v, d, []Change{}})
			sec, inItem = -1, false
			continue
		}
		if len(rs) == 0 {
			continue
		}
		r := &rs[len(rs)-1]
		switch {
		case strings.HasPrefix(line, "### "):
			name := strings.TrimPrefix(line, "### ")
			if sec = slices.IndexFunc(r.Changes, func(c Change) bool { return c.Section == name }); sec < 0 {
				r.Changes = append(r.Changes, Change{name, []string{}})
				sec = len(r.Changes) - 1
			}
			inItem = false
		case sec < 0:
		case strings.HasPrefix(line, "- "):
			r.Changes[sec].Items = append(r.Changes[sec].Items, line[2:])
			inItem = true
		case inItem && strings.HasPrefix(line, "  "):
			items := r.Changes[sec].Items
			items[len(items)-1] += " " + strings.TrimSpace(line)
		default:
			inItem = false
		}
	}
	return slices.DeleteFunc(rs, func(r Release) bool { return r.Version == "Unreleased" && len(r.Changes) == 0 })
}

// Version is the newest cut version, or 0.0.0 before the first.
func Version() string { return info().Version }

// VersionJSON is the GET /api/v1/version body, built once from the binary.
var VersionJSON = sync.OnceValue(func() []byte { b, _ := json.Marshal(info()); return b })

type versionInfo struct {
	Version     string    `json:"version"`
	Commit      string    `json:"commit"`
	Date        string    `json:"date"`
	History     []Release `json:"history"`
	FullHistory string    `json:"full_history"`
}

var info = sync.OnceValue(func() versionInfo {
	v := versionInfo{"0.0.0", Commit, Built, ParseChangelog(Changelog), FullHistory}
	if i := slices.IndexFunc(v.History, func(r Release) bool { return r.Version != "Unreleased" }); i >= 0 {
		v.Version = v.History[i].Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && v.Commit == "" {
				v.Commit = s.Value
			} else if s.Key == "vcs.time" && v.Date == "" {
				v.Date = s.Value
			}
		}
	}
	return v
})
