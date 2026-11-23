// Command cutversion turns the Unreleased entries of CHANGELOG.md into a
// dated version, and keeps the file the binary embeds small by moving the
// oldest versions to CHANGELOG-archive.md (SPEC section 5.2). From the repo
// root: go run ./server/cmd/cutversion [-summary-file F] 0.1.0 [YYYY-MM-DD]
//
// With -summary-file, the file's short hand-written text becomes the
// version's section and the full Unreleased entries go to the archive under
// the same heading.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/thejdubb02/clrd"
)

const (
	changelog = "CHANGELOG.md"
	archive   = "CHANGELOG-archive.md"
	near      = 90000 // archive once a cut leaves the file over this
	keep      = 10    // versions kept while they fit under clrd.ChangelogLimit
)

const archiveHead = "# Changelog archive\n\nOlder versions, newest first, moved out of [CHANGELOG.md](CHANGELOG.md)\nso the server binary carries only the recent ones.\n"

func main() {
	sumFile := flag.String("summary-file", "", "short summary to keep in CHANGELOG.md; the full entries go to the archive")
	flag.Parse()
	if flag.NArg() < 1 || flag.NArg() > 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./server/cmd/cutversion [-summary-file F] <X.Y.Z> [YYYY-MM-DD]")
		os.Exit(2)
	}
	date := time.Now().UTC().Format(time.DateOnly)
	if flag.NArg() == 2 {
		date = flag.Arg(1)
	}
	if err := run(flag.Arg(0), date, *sumFile); err != nil {
		fmt.Fprintln(os.Stderr, "cutversion:", err)
		os.Exit(1)
	}
}

func run(version, date, sumFile string) error {
	summary := ""
	if sumFile != "" {
		b, err := os.ReadFile(sumFile)
		if err != nil {
			return err
		}
		summary = string(b)
	}
	md, err := os.ReadFile(changelog)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(archive)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, arch, err := cut(string(md), string(old), version, date, summary)
	if err != nil {
		return err
	}
	if arch != string(old) {
		if err := os.WriteFile(archive, []byte(arch), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(changelog, []byte(out), 0o644)
}

var semver = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// cut returns the new changelog and archive. A non-empty summary replaces the
// version's entries in the changelog; the entries go to the archive.
func cut(md, arch, version, date, summary string) (string, string, error) {
	if !semver.MatchString(version) {
		return "", "", fmt.Errorf("version %q is not X.Y.Z", version)
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return "", "", fmt.Errorf("date %q is not YYYY-MM-DD", date)
	}
	for _, r := range clrd.ParseChangelog(md + "\n" + arch) {
		if r.Version == version {
			return "", "", fmt.Errorf("%s is already cut", version)
		}
	}
	head, rest, ok := strings.Cut(md, "\n## Unreleased\n")
	if !ok {
		return "", "", errors.New("no ## Unreleased heading")
	}
	body, older := rest, ""
	if i := strings.Index(rest, "\n## "); i >= 0 {
		body, older = rest[:i+1], rest[i+1:]
	}
	if !strings.Contains("\n"+body, "\n- ") {
		return "", "", errors.New("nothing under Unreleased to cut")
	}
	heading := "## " + version + " - " + date + "\n"
	var moved []string // newest first
	if summary != "" {
		summary = "\n" + strings.TrimSpace(summary) + "\n"
		if !strings.Contains(summary, "\n### ") || !strings.Contains(summary, "\n- ") {
			return "", "", errors.New("summary needs ### sections with - items, like Unreleased")
		}
		moved, body = sections(heading+body), summary
	}
	top := head + "\n## Unreleased\n\n"
	vers := sections(heading + body + older)
	size := func() int { return len(top) + len(strings.Join(vers, "")) }
	var old []string // newest first
	for size() > near && len(vers) > 1 && (len(vers) > keep || size() > clrd.ChangelogLimit) {
		old = append([]string{vers[len(vers)-1]}, old...)
		vers = vers[:len(vers)-1]
	}
	moved = append(moved, old...)
	if size() > clrd.ChangelogLimit {
		return "", "", fmt.Errorf("%s alone is over %d bytes; shorten its entries", version, clrd.ChangelogLimit)
	}
	if len(moved) > 0 {
		if arch == "" {
			arch = archiveHead
		}
		h, t, found := strings.Cut(arch, "\n## ")
		if found {
			t = "## " + t
		}
		arch = strings.TrimRight(h, "\n") + "\n\n" + strings.TrimRight(strings.Join(moved, "")+t, "\n") + "\n"
	}
	return strings.TrimRight(top+strings.Join(vers, ""), "\n") + "\n", arch, nil
}

// sections splits s at each "## " heading, each part ending in a blank line.
func sections(s string) []string {
	var out []string
	for s != "" {
		part := s
		if i := strings.Index(s, "\n## "); i >= 0 {
			part = s[:i+1]
		}
		s = s[len(part):]
		out = append(out, strings.TrimRight(part, "\n")+"\n\n")
	}
	return out
}
