// Command cutversion turns the Unreleased entries of CHANGELOG.md into a
// dated version, and keeps the file the binary embeds small by moving the
// oldest versions to CHANGELOG-archive.md (SPEC section 5.2). From the repo
// root: go run ./server/cmd/cutversion 0.1.0 [YYYY-MM-DD]
package main

import (
	"errors"
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
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./server/cmd/cutversion <X.Y.Z> [YYYY-MM-DD]")
		os.Exit(2)
	}
	date := time.Now().UTC().Format(time.DateOnly)
	if len(os.Args) == 3 {
		date = os.Args[2]
	}
	if err := run(os.Args[1], date); err != nil {
		fmt.Fprintln(os.Stderr, "cutversion:", err)
		os.Exit(1)
	}
}

func run(version, date string) error {
	md, err := os.ReadFile(changelog)
	if err != nil {
		return err
	}
	old, err := os.ReadFile(archive)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, arch, err := cut(string(md), string(old), version, date)
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

// cut returns the new changelog and archive.
func cut(md, arch, version, date string) (string, string, error) {
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
	top := head + "\n## Unreleased\n\n"
	vers := sections("## " + version + " - " + date + "\n" + body + older)
	size := func() int { return len(top) + len(strings.Join(vers, "")) }
	var moved []string // newest first
	for size() > near && len(vers) > 1 && (len(vers) > keep || size() > clrd.ChangelogLimit) {
		moved = append([]string{vers[len(vers)-1]}, moved...)
		vers = vers[:len(vers)-1]
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
