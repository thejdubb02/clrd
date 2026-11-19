package server

import (
	"encoding/base64"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWebhookBuiltins sends each built-in its fixture (a vendor example, or
// the exact body its docs page says to paste) and checks what it publishes,
// then sends the follow-up that resolves it where the service has one.
func TestWebhookBuiltins(t *testing.T) {
	_, ts, admin := newTest(t)
	for _, c := range []struct {
		name, hdr, title, prio string
		resolve                [2]string // replace in the fixture to get the event that resolves it; "" = none
	}{
		{"github", "X-GitHub-Event: issues", "octocat/Hello-World: issues opened", "3", [2]string{`"opened"`, `"closed"`}},
		{"sentry", "", "ZeroDivisionError: division by zero", "3", [2]string{`"created"`, `"resolved"`}},
		{"stripe", "", "payment_intent.payment_failed", "4", [2]string{}},
		{"jira", "", "OPS-12: Backups failing on db1", "3", [2]string{`"key":"new"`, `"key":"done"`}},
		{"healthchecks", "", "nightly-backup is down", "4", [2]string{`"down"`, `"up"`}},
		{"proxmox", "", "vzdump backup status (pve1): backup failed", "4", [2]string{}},
		{"zabbix", "", "Problem: High CPU utilization on web1", "4", [2]string{`"value":"1"`, `"value":"0"`}},
		{"netdata", "", "disk_space_usage on disk_space._", "4", [2]string{`"critical"`, `"clear"`}},
		{"jellyfin", "", "The Matrix", "3", [2]string{}},
		{"arr", "", "Sonarr: Health", "3", [2]string{`"Health"`, `"HealthRestored"`}},
	} {
		b, err := os.ReadFile("testdata/webhooks/" + c.name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		hdr := map[string]string{}
		if k, v, ok := strings.Cut(c.hdr, ": "); ok {
			hdr[k] = v
		}
		topic := "wh-" + c.name
		url := ts.URL + "/" + topic + "?template=" + c.name
		if code, out := raw(t, "POST", url, admin, string(b), hdr); code != 200 || strings.TrimSpace(out) != `{"published":1,"resolved":0}` {
			t.Fatalf("%s: %d %s", c.name, code, out)
		}
		_, l := call(t, ts, "GET", "/api/v1/notifications?topic="+topic+"&state=all", admin, nil)
		ns := l["notifications"].([]any)
		if len(ns) != 1 {
			t.Fatalf("%s: %v", c.name, l)
		}
		n := ns[0].(map[string]any)
		if n["title"] != c.title || strconv.Itoa(int(n["priority"].(float64))) != c.prio {
			t.Errorf("%s: title %q priority %v, want %q %s", c.name, n["title"], n["priority"], c.title, c.prio)
		}
		if strings.Contains(n["title"].(string)+n["message"].(string), "<no value>") {
			t.Errorf("%s: unfilled field in %v", c.name, n)
		}
		if c.resolve[0] == "" {
			continue
		}
		if !strings.Contains(string(b), c.resolve[0]) {
			t.Fatalf("%s: fixture has no %s", c.name, c.resolve[0])
		}
		next := strings.Replace(string(b), c.resolve[0], c.resolve[1], 1)
		if code, out := raw(t, "POST", url, admin, next, hdr); code != 200 || strings.TrimSpace(out) != `{"published":0,"resolved":1}` {
			t.Fatalf("%s resolve: %d %s", c.name, code, out)
		}
	}
}

// Published test vectors: GitHub's and Jira's docs give secret, body, signature.
func TestWebhookSignatureVectors(t *testing.T) {
	for _, c := range []struct{ header, body, sig string }{
		{"X-Hub-Signature-256", "Hello, World!", "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"},
		{"X-Hub-Signature", "Hello World!", "sha256=a4771c39fbe90f317c7824e83ddef3caae9cb3d976c214ace1f2937e133263c9"},
	} {
		r, _ := http.NewRequest("POST", "/", nil)
		r.Header.Set(c.header, c.sig)
		if !hubSignature(c.header)(r, []byte(c.body), "It's a Secret to Everybody") {
			t.Errorf("%s: vendor vector refused", c.header)
		}
		if hubSignature(c.header)(r, []byte(c.body+" "), "It's a Secret to Everybody") {
			t.Errorf("%s: changed body accepted", c.header)
		}
	}
}

// TestWebhookSaved: a topic owner sets a built-in's secret, saves a custom
// template, and the secret is checked and never shown.
func TestWebhookSaved(t *testing.T) {
	s, ts, admin := newTest(t)
	pub := mkToken(t, s, "pub", []string{ScopePublish}, []string{"ci"})
	other := mkToken(t, s, "other", []string{ScopePublish}, []string{"elsewhere"})

	if code, _ := call(t, ts, "PUT", "/api/v1/topics/ci/templates/stripe", other, map[string]string{"secret": "x"}); code != 403 {
		t.Fatalf("other topic's token saved a template: %d", code)
	}
	if code, out := call(t, ts, "PUT", "/api/v1/topics/ci/templates/stripe", pub, map[string]string{"secret": "whsec_test"}); code != 200 || out["secret"] != nil {
		t.Fatalf("save stripe secret: %d %v", code, out)
	}
	body, _ := os.ReadFile("testdata/webhooks/stripe.json")
	now := strconv.FormatInt(time.Now().Unix(), 10)
	good := "t=" + now + ",v1=bogus,v1=" + hmacHex("whsec_test", append([]byte(now+"."), body...))
	for _, c := range []struct {
		sig  string
		code int
	}{
		{"", 401},
		{"t=" + now + ",v1=" + hmacHex("wrong", append([]byte(now+"."), body...)), 401},
		{"t=1600000000,v1=" + hmacHex("whsec_test", append([]byte("1600000000."), body...)), 401}, // too old
		{good, 200},
	} {
		if code, out := raw(t, "POST", ts.URL+"/ci?template=stripe", pub, string(body), map[string]string{"Stripe-Signature": c.sig}); code != c.code {
			t.Errorf("stripe sig %q: %d %s", c.sig, code, out)
		}
	}

	// A custom template, with a shared secret since the sender cannot sign.
	tpl := map[string]string{"each": "items", "title": `{{get . "name"}}`, "message": `{{get . "detail.0.text"}} {{header "X-Secret-Note"}}`,
		"id": `{{get . "id"}}`, "resolve": `{{eq (get . "ok") "true"}}`, "priority": `{{if get . "urgent"}}urgent{{end}}`, "secret": "s3cret"}
	for _, bad := range []string{`{{get . }`, `{{range .items}}x{{end}}`, `{{define "x"}}y{{end}}`, `{{template "message" .}}`,
		`{{if true}}{{printf "%999999999d" 1}}{{end}}`, strings.Repeat("x", 17<<10)} {
		if code, out := call(t, ts, "PUT", "/api/v1/topics/ci/templates/my-app", pub, map[string]string{"title": bad}); code != 400 {
			t.Fatalf("template %.40q saved: %d %v", bad, code, out)
		}
	}
	if code, out := call(t, ts, "PUT", "/api/v1/topics/ci/templates/my-app", pub, tpl); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	_, l := call(t, ts, "GET", "/api/v1/topics/ci/templates", pub, nil)
	saved := l["webhooks"].([]any)
	if len(saved) != 2 || saved[0].(map[string]any)["name"] != "my-app" || saved[0].(map[string]any)["has_secret"] != true || saved[0].(map[string]any)["secret"] != nil {
		t.Fatalf("list: %v", l)
	}
	msg := `{"items":[{"id":"a","name":"Disk full","urgent":true,"detail":[{"text":"97%"}]},{"id":"b","name":"No detail"}]}`
	hdr := map[string]string{"X-Clrd-Secret": "s3cret", "X-Secret-Note": "leaked"}
	if code, _ := raw(t, "POST", ts.URL+"/ci?template=my-app&secret=nope", pub, msg, nil); code != 401 {
		t.Fatalf("wrong secret: %d", code)
	}
	if code, out := raw(t, "POST", ts.URL+"/ci?template=my-app", pub, msg, hdr); code != 200 || strings.TrimSpace(out) != `{"published":2,"resolved":0}` {
		t.Fatalf("custom: %d %s", code, out)
	}
	_, l = call(t, ts, "GET", "/api/v1/notifications?topic=ci&q=Disk+full&state=all", admin, nil)
	n := l["notifications"].([]any)[0].(map[string]any)
	if n["message"] != "97%" || n["priority"].(float64) != 5 {
		t.Errorf("custom fields (headers naming a secret must not render): %v", n)
	}
	auth := base64.RawURLEncoding.EncodeToString([]byte("Bearer " + pub)) // what the docs tell services to use
	if code, out := raw(t, "POST", ts.URL+"/ci?template=my-app&secret=s3cret&auth="+auth, "", `{"items":[{"id":"a","ok":true}]}`, nil); code != 200 || strings.TrimSpace(out) != `{"published":0,"resolved":1}` {
		t.Fatalf("custom resolve: %d %s", code, out)
	}

	if code, _ := call(t, ts, "DELETE", "/api/v1/topics/ci/templates/my-app", pub, nil); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if code, _ := raw(t, "POST", ts.URL+"/ci?template=my-app", pub, msg, hdr); code != 400 {
		t.Fatalf("deleted template still used: %d", code)
	}
}
