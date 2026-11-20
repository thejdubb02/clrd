package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

// browser is a client with its own cookies, like one browser.
func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func pairSend(t *testing.T, c *http.Client, method, url, tok string, body any) (int, map[string]any, string) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	json.Unmarshal(raw, &out)
	return res.StatusCode, out, string(raw)
}

func TestPairSignIn(t *testing.T) {
	s, ts, _ := newTest(t)
	phone := mkToken(t, s, "pixel", []string{ScopeRead}, nil)
	pc, other := browser(t), browser(t)

	code, p, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/pair", "", nil)
	pairCode, _ := p["code"].(string)
	if code != 201 || len(pairCode) != 22 || !strings.HasPrefix(p["qr"].(string), "data:image/png;base64,") ||
		!strings.HasSuffix(p["url"].(string), "/pair?c="+pairCode) {
		t.Fatalf("create: %d %v", code, p)
	}
	claim := func(c *http.Client) int {
		code, _, _ := pairSend(t, c, "POST", ts.URL+"/api/v1/pair/claim", "", map[string]string{"code": pairCode})
		return code
	}
	if c := claim(pc); c != 202 {
		t.Fatalf("claim before approve: %d, want 202", c)
	}
	if code, _, _ := pairSend(t, pc, "GET", ts.URL+"/api/v1/pair/"+pairCode, "", nil); code != 401 {
		t.Fatalf("info without a token: %d, want 401", code)
	}
	if code, info, _ := pairSend(t, other, "GET", ts.URL+"/api/v1/pair/"+pairCode, phone, nil); code != 200 || info["name"] != "Web browser" {
		t.Fatalf("info: %d %v", code, info)
	}
	if code, _, _ := pairSend(t, other, "POST", ts.URL+"/api/v1/pair/"+pairCode+"/approve", phone, nil); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if code, _, _ := pairSend(t, other, "POST", ts.URL+"/api/v1/pair/"+pairCode+"/approve", phone, nil); code != 404 {
		t.Fatalf("second approve: %d, want 404", code)
	}
	// The code alone is worth nothing: another browser, even one holding its
	// own pairing cookie, cannot claim it.
	if c := claim(other); c != 410 {
		t.Fatalf("claim from a browser with no cookie: %d, want 410", c)
	}
	pairSend(t, other, "POST", ts.URL+"/api/v1/pair", "", nil)
	if c := claim(other); c != 410 {
		t.Fatalf("claim from another browser: %d, want 410", c)
	}
	if c := claim(pc); c != 200 {
		t.Fatalf("claim after approve: %d, want 200", c)
	}
	if c := claim(pc); c != 410 {
		t.Fatalf("claim twice: %d, want 410", c)
	}
	// Signed in with the phone's access, and no more.
	if code, _, _ := pairSend(t, pc, "GET", ts.URL+"/api/v1/notifications", "", nil); code != 200 {
		t.Fatalf("list as the paired browser: %d", code)
	}
	if code, _, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/notifications", "", map[string]string{"topic": "x", "title": "y"}); code != 403 {
		t.Fatalf("publish as a read-only paired browser: %d, want 403", code)
	}
}

func TestPairDenyAndExpiry(t *testing.T) {
	s, ts, admin := newTest(t)
	pc := browser(t)
	start := func() string {
		_, p, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/pair", "", nil)
		return p["code"].(string)
	}
	claim := func(c string) int {
		code, _, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/pair/claim", "", map[string]string{"code": c})
		return code
	}

	c := start()
	if code, _, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/pair/"+c+"/deny", admin, nil); code != 204 {
		t.Fatalf("deny: %d", code)
	}
	if got := claim(c); got != 410 {
		t.Fatalf("claim after deny: %d, want 410", got)
	}

	c = start()
	s.pairMu.Lock()
	s.pairs[c].created = time.Now().Add(-pairTTL - time.Second)
	s.pairMu.Unlock()
	if code, _, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/pair/"+c+"/approve", admin, nil); code != 404 {
		t.Fatalf("approve expired: %d, want 404", code)
	}
	if got := claim(c); got != 410 {
		t.Fatalf("claim expired: %d, want 410", got)
	}
}

func TestPairPage(t *testing.T) {
	s, ts, admin := newTest(t)
	pc, phone := browser(t), browser(t)
	_, p, _ := pairSend(t, pc, "POST", ts.URL+"/api/v1/pair", "", nil)
	c := p["code"].(string)

	_, _, page := pairSend(t, phone, "GET", ts.URL+"/pair?c="+c, "", nil)
	if !strings.Contains(page, `href="clrd://pair?code=`+c+`&amp;server=`) || strings.Contains(page, "Approve</button>") {
		t.Fatalf("signed out: want the app link and no approve button:\n%s", page)
	}
	_, _, page = pairSend(t, phone, "GET", ts.URL+"/pair?c="+c, admin, nil)
	if !strings.Contains(page, "Approve</button>") {
		t.Fatalf("signed in: want approve:\n%s", page)
	}
	_, _, page = pairSend(t, phone, "GET", ts.URL+"/pair?c=nope", "", nil)
	if !strings.Contains(page, "expired") {
		t.Fatalf("unknown code: %s", page)
	}
	if len(s.pairs) != 1 {
		t.Fatalf("pairs: %d", len(s.pairs))
	}
}
