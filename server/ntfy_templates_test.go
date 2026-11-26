package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNtfyNamedTemplate(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".yml"), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("alarm", `title: 'Alert: {{ .name }}' # owner note
message: |-
  Level: {{ .level }}
  ID: {{ .id }}
priority: "{{ if .urgent }}5{{ else }}3{{ end }}"
`)
	write("limited", "title: '{{ .name }}'\n")
	write("priority-only", "priority: 4\n")
	write("invalid", "message: [a, b]\n")
	write("range", "message: '{{range .}}x{{end}}'\n")
	write("large", "message: "+strings.Repeat("x", ntfyTemplateFileMax))

	t.Setenv("CLRD_NTFY_TEMPLATE_DIR", dir)
	opt, err := ConfigFromEnv()
	if err != nil || opt.NtfyTemplateDir != dir {
		t.Fatalf("template dir config: %q %v", opt.NtfyTemplateDir, err)
	}
	opt.DB = filepath.Join(t.TempDir(), "clrd.db")
	opt.NtfyImplicitUploads = true // named bodies still use the JSON path and its limit
	s, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s)
	defer ts.Close()
	pub := mkToken(t, s, "alarm", []string{ScopePublish}, []string{"alerts"})
	admin := mkToken(t, s, "admin", []string{ScopeAdmin}, nil)
	const payload = `{"name":"disk","level":"critical","id":9007199254740993,"urgent":true}`
	for _, v := range []struct {
		path string
		hdr  map[string]string
	}{
		{"/alerts?template=alarm", nil},
		{"/alerts?tpl=alarm", nil},
		{"/alerts", map[string]string{"X-Template": "alarm"}},
	} {
		code, out := raw(t, "POST", ts.URL+v.path, pub, payload, v.hdr)
		if code != 200 {
			t.Fatalf("publish %s: %d %s", v.path, code, out)
		}
		m := ntfyDecode(t, out)
		if m.Title != "Alert: disk" || m.Message != "Level: critical\nID: 9007199254740993" || m.Priority != 5 {
			t.Fatalf("bad named template output: %+v", m)
		}
	}
	code, out := raw(t, "PUT", ts.URL+"/alerts?template=limited", pub, payload, map[string]string{"X-Message": "fallback", "X-Priority": "2"})
	if code != 200 {
		t.Fatalf("partial template: %d %s", code, out)
	}
	if m := ntfyDecode(t, out); m.Title != "disk" || m.Message != "fallback" || m.Priority != 2 {
		t.Fatalf("partial template replaced fallback fields: %+v", m)
	}

	if code, out := raw(t, "PUT", ts.URL+"/alerts?template=priority-only", pub, payload, nil); code != 200 || ntfyDecode(t, out).Message != "triggered" {
		t.Fatalf("priority-only file should use ntfy's empty message default: %d %s", code, out)
	}

	for _, v := range []struct {
		path, token, body, want string
		code                    int
	}{
		{"/elsewhere?template=alarm", pub, payload, "may not publish", 403},
		{"/alerts?template=alarm", "", payload, "missing or invalid token", 401},
		{"/alerts?template=missing", pub, payload, "unknown named template", 400},
		{"/alerts?template=..%2Falarm", pub, payload, "invalid named template", 400},
		{"/alerts?template=invalid", pub, payload, "plain or quoted", 400},
		{"/alerts?template=range", pub, payload, "range is not allowed", 400},
		{"/alerts?template=large", pub, payload, "over 32 KB", 400},
		{"/alerts?template=alarm", pub, "not JSON", "body must be JSON", 400},
		{"/alerts?template=alarm", pub, payload + "{}", "one JSON value", 400},
		{"/alerts?template=alarm", pub, strings.Repeat("x", ntfyMaxBody+1), "message longer", 413},
	} {
		if code, out := raw(t, "POST", ts.URL+v.path, v.token, v.body, nil); code != v.code || !strings.Contains(out, v.want) {
			t.Errorf("%s: got %d %s, want %d with %q", v.path, code, out, v.code, v.want)
		}
	}
	if code, out := call(t, ts, "PUT", "/api/v1/topics/alerts/templates/alarm", admin,
		map[string]any{"message": "saved {{ .name }}", "secret": "a-shared-test-secret"}); code != 200 {
		t.Fatalf("save overlapping webhook: %d %v", code, out)
	}
	if code, out := raw(t, "POST", ts.URL+"/alerts?template=alarm", pub, payload, nil); code != 401 || !strings.Contains(out, "signature or secret") {
		t.Fatalf("named file should not bypass saved webhook credentials: %d %s", code, out)
	}
	t.Run("symlink", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(dir, "limited.yml"), filepath.Join(dir, "alias.yml")); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if code, out := raw(t, "POST", ts.URL+"/alerts?template=alias", pub, payload, nil); code != 400 || !strings.Contains(out, "regular .yml file") {
			t.Fatalf("symlink selected a file: %d %s", code, out)
		}
	})
}

func TestNtfyNamedTemplateOff(t *testing.T) {
	_, ts, admin := newTest(t)
	const body = `{"name":"disk"}`
	if code, out := raw(t, "POST", ts.URL+"/alerts", admin, body, map[string]string{"X-Template": "alarm"}); code != 200 || ntfyDecode(t, out).Message != body {
		t.Fatalf("an unconfigured disk selector changed an ordinary publish: %d %s", code, out)
	}
	if code, out := raw(t, "POST", ts.URL+"/alerts?template=alarm", admin, body, nil); code != 400 || !strings.Contains(out, "use a built-in") {
		t.Fatalf("unconfigured lookup changed existing webhook behavior: %d %s", code, out)
	}
}
