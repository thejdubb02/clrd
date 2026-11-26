package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR fake image")

// upload sends a native multipart publish. fileFirst puts the parts in the wrong order.
func upload(t *testing.T, ts *httptest.Server, tok string, notif map[string]any, name string, content []byte, fileFirst bool) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	addJSON := func() {
		w, _ := mw.CreateFormField("notification")
		json.NewEncoder(w).Encode(notif)
	}
	if !fileFirst {
		addJSON()
	}
	w, _ := mw.CreateFormFile("file", name)
	w.Write(content)
	if fileFirst {
		addJSON()
	}
	mw.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/notifications", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// download fetches a file URL (relative or absolute) with a Bearer token.
func download(t *testing.T, ts *httptest.Server, u, tok string) (*http.Response, []byte) {
	t.Helper()
	if strings.HasPrefix(u, "/") {
		u = ts.URL + u
	}
	req, _ := http.NewRequest("GET", u, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func attachment(t *testing.T, n map[string]any) map[string]any {
	t.Helper()
	a, ok := n["attachment"].(map[string]any)
	if !ok {
		t.Fatalf("no attachment in %v", n)
	}
	return a
}

func files(t *testing.T, s *Server) []string {
	t.Helper()
	es, err := os.ReadDir(s.opt.Attachments)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestUploadAndDownload(t *testing.T) {
	s, ts, admin := newTest(t)
	code, n := upload(t, ts, admin, map[string]any{"topic": "infra", "title": "graph"}, `..\..\evil/chart.png`, pngBytes, false)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, n)
	}
	a := attachment(t, n)
	if a["name"] != "chart.png" || a["type"] != "image/png" || a["size"] != float64(len(pngBytes)) ||
		!strings.HasPrefix(a["url"].(string), "/api/v1/files/") {
		t.Fatalf("attachment: %v", a)
	}
	if f := files(t, s); len(f) != 1 || "/api/v1/files/"+f[0] != a["url"] {
		t.Fatalf("files on disk: %v", f)
	}
	res, body := download(t, ts, a["url"].(string), admin)
	if res.StatusCode != 200 || !bytes.Equal(body, pngBytes) || res.Header.Get("Content-Type") != "image/png" ||
		!strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline") ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("download: %d %v", res.StatusCode, res.Header)
	}

	// Read scope on another topic, or no token: no file.
	other := mkToken(t, s, "other", []string{ScopeRead}, []string{"home"})
	if res, _ := download(t, ts, a["url"].(string), other); res.StatusCode != 403 {
		t.Fatalf("other topic: %d", res.StatusCode)
	}
	if res, _ := download(t, ts, a["url"].(string), ""); res.StatusCode != 401 {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	for _, bad := range []string{"/api/v1/files/..%2Fclrd.db", "/api/v1/files/" + strings.Repeat("0", 32)} {
		if res, _ := download(t, ts, bad, admin); res.StatusCode != 404 {
			t.Fatalf("%s: %d", bad, res.StatusCode)
		}
	}

	// HTML named .png is sniffed as HTML and served as a download, never inline.
	_, n = upload(t, ts, admin, map[string]any{"topic": "infra"}, `a"b.png`, []byte("<html><script>alert(1)</script>"), false)
	res, _ = download(t, ts, attachment(t, n)["url"].(string), admin)
	disp, params, err := mime.ParseMediaType(res.Header.Get("Content-Disposition"))
	if err != nil || disp != "attachment" || params["filename"] != `a"b.png` || res.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("html download: %v %v %v", res.Header, params, err)
	}
}

func TestUploadRefusalsStoreNothing(t *testing.T) {
	s, ts, admin := newTest(t)
	other := mkToken(t, s, "other", []string{ScopePublish}, []string{"home"})
	s.opt.AttachmentMax = 64
	for _, c := range []struct {
		name  string
		tok   string
		notif map[string]any
		body  []byte
		first bool
		want  int
	}{
		{"no token", "", map[string]any{"topic": "infra"}, pngBytes, false, 401},
		{"wrong topic", other, map[string]any{"topic": "infra"}, pngBytes, false, 403},
		{"over the cap", admin, map[string]any{"topic": "infra"}, bytes.Repeat([]byte("x"), 65), false, 413},
		{"status resolved", admin, map[string]any{"topic": "infra", "external_id": "k", "status": "resolved"}, pngBytes, false, 400},
		{"file part first", admin, map[string]any{"topic": "infra"}, pngBytes, true, 400},
		{"bad publish", admin, map[string]any{"topic": "infra", "priority": 9}, pngBytes, false, 400},
	} {
		code, out := upload(t, ts, c.tok, c.notif, "f.bin", c.body, c.first)
		if code != c.want {
			t.Errorf("%s: %d %v", c.name, code, out)
		}
		if f := files(t, s); len(f) != 0 {
			t.Errorf("%s left files: %v", c.name, f)
		}
	}
	if code, _ := upload(t, ts, admin, map[string]any{"topic": "infra", "external_id": "q"}, "f.bin", bytes.Repeat([]byte("x"), 64), false); code != 200 {
		t.Fatalf("at the cap: %d", code)
	}
	s.opt.AttachmentTotal = 100 // 64 already stored
	if code, out := upload(t, ts, admin, map[string]any{"topic": "infra"}, "f.bin", bytes.Repeat([]byte("x"), 40), false); code != 507 || len(files(t, s)) != 1 {
		t.Fatalf("over the total: %d %v %v", code, out, files(t, s))
	}
	// Replacing the stored file frees its space, so this fits.
	if code, out := upload(t, ts, admin, map[string]any{"topic": "infra", "external_id": "q"}, "f.bin", bytes.Repeat([]byte("x"), 40), false); code != 200 || len(files(t, s)) != 1 {
		t.Fatalf("replace at the total: %d %v %v", code, out, files(t, s))
	}
}

func TestExternalAttachment(t *testing.T) {
	_, ts, admin := newTest(t)
	code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra",
		"attachment": map[string]any{"url": "https://example.com/r/report.pdf?x=1", "type": "image/png", "size": 5}})
	if a := attachment(t, n); code != 200 || a["name"] != "report.pdf" || a["type"] != "" || a["size"] != 0.0 {
		t.Fatalf("external: %d %v", code, n)
	}
	for _, u := range []string{"/api/v1/files/" + strings.Repeat("a", 32), "javascript:alert(1)"} {
		if code, _ := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra",
			"attachment": map[string]any{"url": u}}); code != 400 {
			t.Errorf("%s: %d", u, code)
		}
	}
	if code, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "m"}); code != 200 || n["attachment"] != nil || !hasKey(n, "attachment") {
		t.Fatalf("none should be null: %v", n["attachment"])
	}
}

func TestNtfyFiles(t *testing.T) {
	_, ts, admin := newTest(t)
	big := strings.Repeat("y", 10000) // over ntfy's message cap, fine as a file
	code, out := raw(t, "PUT", ts.URL+"/alerts?f="+url.QueryEscape("log\r\nX-Evil: 1.txt"), admin, big, nil)
	if code != 200 {
		t.Fatalf("ntfy upload: %d %s", code, out)
	}
	m := ntfyDecode(t, out)
	if m.Attachment == nil || m.Message != "You received a file: logX-Evil: 1.txt" || m.Attachment.Size != 10000 ||
		!strings.HasPrefix(m.Attachment.URL, "http://localhost:8080/api/v1/files/") {
		t.Fatalf("ntfy message: %+v %+v", m, m.Attachment)
	}
	res, body := download(t, ts, strings.TrimPrefix(m.Attachment.URL, "http://localhost:8080"), admin)
	if res.StatusCode != 200 || string(body) != big || len(res.Header.Values("X-Evil")) != 0 {
		t.Fatalf("download: %d %v", res.StatusCode, res.Header)
	}
	if code, _ := raw(t, "PUT", ts.URL+"/alerts", admin, big, nil); code != 413 {
		t.Fatalf("big body without a file name: %d", code)
	}
	// Over the 1 MiB buffer, to the update-by-id route.
	if code, out := raw(t, "PUT", ts.URL+"/alerts/k?f=big.bin", admin, strings.Repeat("z", 1<<20+10), nil); code != 200 {
		t.Fatalf("upload to /{topic}/{sid}: %d %s", code, out)
	}
	_, out = raw(t, "POST", ts.URL+"/alerts", admin, "see", map[string]string{"X-Attach": "https://example.com/a.zip"})
	if m := ntfyDecode(t, out); m.Message != "see" || m.Attachment == nil || m.Attachment.Name != "a.zip" {
		t.Fatalf("X-Attach: %s", out)
	}
	if code, out = raw(t, "POST", ts.URL+"/alerts", admin, "see", map[string]string{"X-Attach": "ftp://x"}); code != 200 || ntfyDecode(t, out).Attachment != nil {
		t.Fatalf("bad X-Attach kept: %s", out)
	}
	_, out = raw(t, "POST", ts.URL+"/", admin, `{"topic":"alerts","message":"m","attach":"https://example.com/x","filename":"x.pdf"}`, nil)
	if m := ntfyDecode(t, out); m.Attachment == nil || m.Attachment.Name != "x.pdf" {
		t.Fatalf("JSON attach: %s", out)
	}
}

func TestAttachmentLifetime(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	_, n := upload(t, ts, admin, map[string]any{"topic": "infra", "external_id": "k"}, "a.png", pngBytes, false)
	first := attachment(t, n)["url"].(string)
	// Republishing without a file clears it and deletes the old one.
	_, n = call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "external_id": "k", "message": "m"})
	if n["attachment"] != nil || len(files(t, s)) != 0 {
		t.Fatalf("replace: %v %v", n["attachment"], files(t, s))
	}
	if res, _ := download(t, ts, first, admin); res.StatusCode != 404 {
		t.Fatalf("replaced file: %d", res.StatusCode)
	}

	_, n = upload(t, ts, admin, map[string]any{"topic": "infra"}, "a.png", pngBytes, false)
	u, id := attachment(t, n)["url"].(string), n["id"].(string)
	call(t, ts, "POST", "/api/v1/notifications/"+id+"/dismiss", admin, nil)
	old := time.Now().Add(-2 * time.Hour)
	for _, f := range files(t, s) {
		os.Chtimes(filepath.Join(s.opt.Attachments, f), old, old)
	}
	// Leftovers: an old upload cut off by a crash goes, a fresh one stays,
	// and a file that is not ours is never touched.
	for name, at := range map[string]time.Time{".up-old": old, ".up-new": time.Now(), "notes.txt": old} {
		p := filepath.Join(s.opt.Attachments, name)
		os.WriteFile(p, []byte("x"), 0o600)
		os.Chtimes(p, at, at)
	}
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if res, _ := download(t, ts, u, admin); res.StatusCode != 200 {
		t.Fatalf("dismissed within retention: %d", res.StatusCode)
	}
	if f := files(t, s); len(f) != 3 || f[0] != ".up-new" || f[2] != "notes.txt" {
		t.Fatalf("after sweep: %v", f)
	}
	s.db.Exec(`UPDATE notifications SET updated_at = 0 WHERE id = ?`, id)
	if err := s.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if res, _ := download(t, ts, u, admin); res.StatusCode != 404 {
		t.Fatalf("pruned: %d", res.StatusCode)
	}
	if f := files(t, s); len(f) != 2 || f[0] != ".up-new" || f[1] != "notes.txt" {
		t.Fatalf("pruned file kept: %v", f)
	}
}

// An upload may take longer than the 30 s body deadline.
func TestSlowUploadOutlivesBodyDeadline(t *testing.T) {
	_, ts, admin := newTest(t)
	old := bodyTimeout
	bodyTimeout = 100 * time.Millisecond
	defer func() { bodyTimeout = old }()
	pr, pw := io.Pipe()
	go func() {
		pw.Write([]byte("first"))
		time.Sleep(400 * time.Millisecond)
		pw.Write([]byte(" second"))
		pw.Close()
	}()
	req, _ := http.NewRequest("PUT", ts.URL+"/alerts", pr)
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Filename", "slow.txt")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || ntfyDecode(t, string(b)).Attachment.Size != int64(len("first second")) {
		t.Fatalf("slow upload: %d %s", res.StatusCode, b)
	}
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func TestNtfyImplicitUploads(t *testing.T) {
	s, ts, admin := newTest(t)
	big := "<html>" + strings.Repeat("x", maxBody)
	if code, _ := raw(t, "PUT", ts.URL+"/alerts/k", admin, big, nil); code != 413 {
		t.Fatalf("implicit uploads were enabled by default: %d", code)
	}
	t.Setenv("CLRD_NTFY_IMPLICIT_UPLOADS", "on")
	options, err := ConfigFromEnv()
	if err != nil || !options.NtfyImplicitUploads {
		t.Fatalf("implicit uploads option: %v", err)
	}
	s.opt.NtfyImplicitUploads = options.NtfyImplicitUploads

	for _, c := range []struct{ path, body, contentType string }{
		{"/alerts/k", big, "application/octet-stream"},
		{"/message", string(pngBytes), "image/png"},
	} {
		code, out := raw(t, "POST", ts.URL+c.path, admin, c.body, nil)
		if code != 200 {
			t.Fatalf("implicit upload: %d %s", code, out)
		}
		m := ntfyDecode(t, out)
		if m.Attachment == nil || m.Attachment.Name != "attachment" ||
			m.Attachment.Size != int64(len(c.body)) || m.Message != "You received a file: attachment" {
			t.Fatalf("implicit attachment: %s", out)
		}
		u, err := url.Parse(m.Attachment.URL)
		if err != nil {
			t.Fatal(err)
		}
		res, body := download(t, ts, u.Path, admin)
		if res.StatusCode != 200 || string(body) != c.body || res.Header.Get("Content-Type") != c.contentType {
			t.Fatalf("implicit download: %d %v", res.StatusCode, res.Header)
		}
	}
	code, out := raw(t, "POST", ts.URL+"/alerts", admin, strings.Repeat("x", ntfyMaxBody), nil)
	if code != 200 || ntfyDecode(t, out).Attachment != nil {
		t.Fatalf("short text became a file: %d", code)
	}
	for _, h := range []map[string]string{
		{"X-Template": "yes"},
		{"X-Attach": "https://example.com/file"},
	} {
		if code, _ := raw(t, "POST", ts.URL+"/alerts", admin, strings.Repeat("x", ntfyMaxBody+1), h); code != 413 {
			t.Fatalf("text-only publish was inferred as a file: %d %v", code, h)
		}
	}
}

func TestNtfyImplicitUploadsKeepLimits(t *testing.T) {
	s, ts, admin := newTest(t)
	s.opt.NtfyImplicitUploads = true
	s.opt.AttachmentMax = int64(len(pngBytes) - 1)
	url := ts.URL + "/alerts"
	for _, c := range []struct {
		token string
		want  int
	}{{"", 401}, {mkToken(t, s, "other", []string{ScopePublish}, []string{"other"}), 403}, {admin, 413}} {
		if code, out := raw(t, "PUT", url, c.token, string(pngBytes), nil); code != c.want {
			t.Errorf("implicit upload status %d, want %d: %s", code, c.want, out)
		}
	}
	s.opt.AttachmentMax = int64(len(pngBytes))
	s.opt.AttachmentTotal = s.opt.AttachmentMax - 1
	if code, out := raw(t, "PUT", url, admin, string(pngBytes), nil); code != 507 {
		t.Errorf("implicit upload exceeded total quota: %d %s", code, out)
	}
	if f := files(t, s); len(f) != 0 {
		t.Fatalf("refused implicit uploads left files: %v", f)
	}
}
