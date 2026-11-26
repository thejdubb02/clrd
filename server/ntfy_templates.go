package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const ntfyTemplateFileMax = 32 << 10

func readNtfyTemplate(dir, name string) (map[string]string, error) {
	if !webhookNameRe.MatchString(name) {
		return nil, errf(400, "bad_request", "invalid named template: use letters, digits, _ and -, at most 64")
	}
	bad := errf(400, "bad_request", "named template %q is unreadable or is not a regular .yml file", name)
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, bad
	}
	defer root.Close()
	file := name + ".yml"
	info, err := root.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errf(400, "bad_request", "unknown named template %q", name)
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, bad
	}
	f, err := root.Open(file)
	if err != nil {
		return nil, bad
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, ntfyTemplateFileMax+1))
	if err != nil {
		return nil, bad
	}
	if len(b) > ntfyTemplateFileMax {
		return nil, errf(400, "bad_request", "named template %q is over 32 KB", name)
	}
	fields, err := parseNtfyTemplateFile(string(b))
	if err != nil {
		return nil, errf(400, "bad_request", "named template %q: %v", name, err)
	}
	return fields, nil
}

// parseNtfyTemplateFile reads a flat YAML mapping of the three ntfy fields.
// Keeping to single-line scalars and literal blocks avoids pulling a general
// YAML decoder into the server for this optional format.
func parseNtfyTemplateFile(src string) (map[string]string, error) {
	rows := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	fields := map[string]string{}
	for i := 0; i < len(rows); i++ {
		row := strings.TrimSpace(rows[i])
		if row == "" || strings.HasPrefix(row, "#") {
			continue
		}
		if (i == 0 && row == "---") || (row == "..." && strings.TrimSpace(strings.Join(rows[i+1:], "\n")) == "") {
			continue
		}
		key, value, ok := strings.Cut(rows[i], ":")
		key = strings.TrimRight(key, " ")
		if !ok || (key != "message" && key != "title" && key != "priority") {
			return nil, fmt.Errorf("line %d: expected a title, message or priority field", i+1)
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("line %d: repeated %s field", i+1, key)
		}
		value = strings.TrimSpace(value)
		blockStyle := strings.HasPrefix(value, "|")
		value, err := ntfyYAMLScalar(value)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if blockStyle {
			start := i + 1
			for i+1 < len(rows) && (strings.TrimSpace(rows[i+1]) == "" || strings.HasPrefix(rows[i+1], " ")) {
				i++
			}
			block, err := ntfyYAMLBlock(rows[start : i+1])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			value = block
		}
		fields[key] = value
	}
	if len(fields) == 0 {
		return nil, errors.New("expected at least one template field")
	}
	return fields, nil
}

// ntfyYAMLScalar supports plain and single-quoted YAML strings and
// double-quoted strings using JSON escapes.
func ntfyYAMLScalar(s string) (string, error) {
	bad := errors.New("use a plain or quoted value, or an indented literal block")
	switch {
	case strings.HasPrefix(s, "\""):
		d := json.NewDecoder(strings.NewReader(s))
		var value string
		if d.Decode(&value) == nil && yamlComment(s[d.InputOffset():]) {
			return value, nil
		}
	case strings.HasPrefix(s, "'"):
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else if yamlComment(s[i+1:]) {
					return strings.ReplaceAll(s[1:i], "''", "'"), nil
				} else {
					break
				}
			}
		}
	default:
		for i := 0; i < len(s); i++ {
			if s[i] == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t') {
				s = strings.TrimSpace(s[:i])
				break
			}
		}
		if s == "|" || s == "|-" || s == "|+" || (s != "" && !strings.ContainsRune("!&*[]{},>|@`%", rune(s[0])) &&
			!strings.Contains(s, ": ") && !strings.Contains(s, ":\t") && !strings.HasSuffix(s, ":")) {
			return s, nil
		}
	}
	return "", bad
}

func yamlComment(s string) bool {
	return s == "" || (s[0] == ' ' || s[0] == '\t') && (strings.TrimSpace(s) == "" || strings.HasPrefix(strings.TrimSpace(s), "#"))
}

func ntfyYAMLBlock(rows []string) (string, error) {
	indent := 0
	for i, line := range rows {
		if strings.TrimSpace(line) == "" {
			rows[i] = ""
			continue
		}
		if indent == 0 {
			indent = len(line) - len(strings.TrimLeft(line, " "))
		}
		if len(line)-len(strings.TrimLeft(line, " ")) < indent {
			return "", errors.New("inconsistent literal block indentation")
		}
		rows[i] = line[indent:]
	}
	return strings.Join(rows, "\n"), nil
}
