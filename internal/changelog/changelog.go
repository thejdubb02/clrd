// Package changelog assembles pending entries with the released history.
package changelog

import (
	"errors"
	"io/fs"
	"strings"
)

// Assemble adds the pending fragments below Unreleased. It also returns the
// paths a successful release cut should consume. Legacy inline entries are
// kept during the transition.
func Assemble(fsys fs.FS) (string, []string, error) {
	b, err := fs.ReadFile(fsys, "CHANGELOG.md")
	if err != nil {
		return "", nil, err
	}
	const heading = "\n## Unreleased\n"
	head, rest, ok := strings.Cut(string(b), heading)
	if !ok {
		return "", nil, errors.New("CHANGELOG.md needs an Unreleased heading")
	}
	var pending strings.Builder
	var files []string
	for _, section := range [...]string{"Added", "Changed", "Fixed", "Removed", "Security"} {
		dir := "changelog.d/" + strings.ToLower(section)
		entries, err := fs.ReadDir(fsys, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		wroteHeading := false
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			name := dir + "/" + entry.Name()
			body, err := fs.ReadFile(fsys, name)
			if err != nil {
				return "", nil, err
			}
			if !wroteHeading {
				pending.WriteString("\n### " + section + "\n\n")
				wroteHeading = true
			}
			pending.WriteString(strings.TrimRight(string(body), "\n") + "\n")
			files = append(files, name)
		}
	}
	if len(files) == 0 {
		return string(b), nil, nil
	}
	return head + heading + pending.String() + rest, files, nil
}
