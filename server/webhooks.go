package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
)

// Webhooks in (issue #111): POST /{topic}?template=<name> turns any
// service's JSON into notifications. A template maps the JSON to
// notification fields with Go text/template. Built-ins cover common
// services; a topic owner can save their own, or set a built-in's secret and
// override some of its fields, with PUT /api/v1/topics/{topic}/templates/{name}.

// Webhook is one template. Every field but Each and Secret is a Go template
// run over the decoded JSON (or over each item of Each).
type Webhook struct {
	Each     string `json:"each,omitempty"` // dotted path to an array: one notification per item
	Title    string `json:"title,omitempty"`
	Message  string `json:"message,omitempty"`
	Priority string `json:"priority,omitempty"` // renders to 1-5 or min, low, default, high, urgent; "" = 3
	Tags     string `json:"tags,omitempty"`     // renders to a comma separated list
	Click    string `json:"click,omitempty"`
	ID       string `json:"id,omitempty"`      // external_id: the same id updates one notification
	Resolve  string `json:"resolve,omitempty"` // renders to "true" to resolve the notification with ID
	Secret   string `json:"secret,omitempty"`  // never returned by the API
	sign     signer // how the service signs; nil = the secret comes back in X-Clrd-Secret or ?secret=
}

var webhookFields = []string{"title", "message", "priority", "tags", "click", "id", "resolve"}

func (h *Webhook) field(name string) *string {
	return map[string]*string{"title": &h.Title, "message": &h.Message, "priority": &h.Priority, "tags": &h.Tags,
		"click": &h.Click, "id": &h.ID, "resolve": &h.Resolve}[name]
}

// signer reports whether r carries a valid signature of body under secret.
type signer func(r *http.Request, body []byte, secret string) bool

func hmacHex(secret string, msg []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(msg)
	return hex.EncodeToString(m.Sum(nil))
}

func same(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// sharedSecret: services that cannot sign send the secret back as is.
func sharedSecret(r *http.Request, _ []byte, secret string) bool {
	got := r.Header.Get("X-Clrd-Secret")
	if got == "" {
		got = r.URL.Query().Get("secret")
	}
	return same(got, secret)
}

// hubSignature: GitHub's X-Hub-Signature-256 and Jira's X-Hub-Signature,
// "sha256=" + hex HMAC-SHA256 of the body.
func hubSignature(header string) signer {
	return func(r *http.Request, body []byte, secret string) bool {
		return same(r.Header.Get(header), "sha256="+hmacHex(secret, body))
	}
}

// sentrySignature: Sentry-Hook-Signature is the hex HMAC-SHA256 of the body.
func sentrySignature(r *http.Request, body []byte, secret string) bool {
	return same(r.Header.Get("Sentry-Hook-Signature"), hmacHex(secret, body))
}

// stripeSignature: Stripe-Signature "t=<unix>,v1=<hex>[,v1=...]", the HMAC
// of "<t>.<body>", refused when t is more than 5 minutes off (Stripe's default).
func stripeSignature(r *http.Request, body []byte, secret string) bool {
	var ts string
	var sigs []string
	for _, kv := range strings.Split(r.Header.Get("Stripe-Signature"), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || time.Since(time.Unix(t, 0)).Abs() > 5*time.Minute {
		return false
	}
	want := hmacHex(secret, append([]byte(ts+"."), body...))
	return slices.ContainsFunc(sigs, func(s string) bool { return same(s, want) })
}

// get walks dotted paths ("issue.title", "alerts.0.status") and returns the
// first that is set, as text. Missing keys are "", never an error, so one
// template can serve every event type a service sends.
func get(v any, paths ...string) string {
	for _, p := range paths {
		if s := text(walk(v, p)); s != "" {
			return s
		}
	}
	return ""
}

func walk(v any, path string) any {
	for _, k := range strings.Split(path, ".") {
		switch t := v.(type) {
		case map[string]any:
			v = t[k]
		case []any:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(t) {
				return nil
			}
			v = t[i]
		default:
			return nil
		}
	}
	return v
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case map[string]any, []any:
		b, _ := json.Marshal(t)
		return string(b)
	}
	return fmt.Sprint(v)
}

// hiddenHeader: credentials a template must never copy into a notification.
func hiddenHeader(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"auth", "cookie", "secret", "signature", "token", "key"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

func webhookFuncs(h http.Header) template.FuncMap {
	return template.FuncMap{
		"get": get,
		"header": func(name string) string {
			if hiddenHeader(name) {
				return ""
			}
			return h.Get(name)
		},
		"contains": func(s any, sub string) bool { return strings.Contains(text(s), sub) },
		"lower":    func(s any) string { return strings.ToLower(text(s)) },
		"split":    func(s any, sep string) []string { return strings.Split(text(s), sep) },
	}
}

// parse compiles every field; a PUT runs it so a bad template is refused
// when saved, not when the service sends.
func (h *Webhook) parse(hdr http.Header) (*template.Template, error) {
	root := template.New("").Funcs(webhookFuncs(hdr)).Option("missingkey=zero")
	for _, f := range webhookFields {
		src := *h.field(f)
		if len(src) > webhookFieldMax {
			return nil, errf(400, "bad_request", "template %s: over 16 KB", f)
		}
		if _, err := root.New(f).Parse(src); err != nil {
			return nil, errf(400, "bad_request", "template %s: %v", f, err)
		}
	}
	if len(root.Templates()) > len(webhookFields) {
		return nil, errf(400, "bad_request", "template: define and block are not allowed")
	}
	for _, t := range root.Templates() { // after all fields: a define in one field can replace another
		if err := plain(t.Root); err != nil {
			return nil, errf(400, "bad_request", "template %s: %v", t.Name(), err)
		}
	}
	return root, nil
}

// plain refuses what lets a saved template burn CPU or memory without
// writing output: loops (each covers lists), calls to other templates, and
// printf, whose widths allocate before the output cap sees them.
func plain(n parse.Node) error {
	switch t := n.(type) {
	case *parse.RangeNode:
		return errors.New("range is not allowed: use each")
	case *parse.TemplateNode:
		return errors.New("template calls are not allowed")
	case *parse.IdentifierNode:
		if t.Ident == "printf" {
			return errors.New("printf is not allowed")
		}
	case *parse.ListNode:
		if t == nil {
			return nil
		}
		for _, c := range t.Nodes {
			if err := plain(c); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return plain(t.Pipe)
	case *parse.PipeNode:
		if t == nil {
			return nil
		}
		for _, c := range t.Cmds {
			if err := plain(c); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		for _, a := range t.Args {
			if err := plain(a); err != nil {
				return err
			}
		}
	case *parse.IfNode:
		return branch(&t.BranchNode)
	case *parse.WithNode:
		return branch(&t.BranchNode)
	}
	return nil
}

func branch(b *parse.BranchNode) error {
	for _, n := range []parse.Node{b.Pipe, b.List, b.ElseList} {
		if err := plain(n); err != nil {
			return err
		}
	}
	return nil
}

// capped stops a template that renders without end.
type capped struct{ bytes.Buffer }

const webhookFieldMax = 16 << 10

// webhookItemsMax caps one delivery's fan-out; Alertmanager groups stay far below it.
const webhookItemsMax = 1000

func (c *capped) Write(p []byte) (int, error) {
	if c.Len()+len(p) > webhookFieldMax {
		return 0, errors.New("output over 16 KB")
	}
	return c.Buffer.Write(p)
}

func render(t *template.Template, field string, data any) (string, error) {
	var b capped
	if err := t.ExecuteTemplate(&b, field, data); err != nil {
		return "", errf(400, "bad_request", "template %s: %v", field, err)
	}
	// A missing key in a JSON object prints "<no value>" even with missingkey=zero.
	return strings.TrimSpace(strings.ReplaceAll(b.String(), "<no value>", "")), nil
}

var webhookNameRe = topicRe

// webhook finds the template name for topic: the topic's own saved one,
// with any field it leaves empty taken from the built-in of that name.
func (s *Server) webhook(ctx context.Context, topic, name string) (*Webhook, error) {
	var spec string
	err := s.db.QueryRowContext(ctx, `SELECT spec FROM webhook_templates WHERE topic = ? AND name = ?`, topic, name).Scan(&spec)
	b, builtin := builtinWebhooks[name]
	if errors.Is(err, sql.ErrNoRows) {
		if !builtin {
			return nil, errf(400, "bad_request", "unknown template %q: use a built-in (%s) or save one for this topic",
				name, strings.Join(builtinNames(), ", "))
		}
		return &b, nil
	}
	if err != nil {
		return nil, err
	}
	var h Webhook
	if err := json.Unmarshal([]byte(spec), &h); err != nil {
		return nil, err
	}
	h.overBuiltin(name)
	return &h, nil
}

// overBuiltin fills the fields h leaves empty from the built-in named name, if any.
func (h *Webhook) overBuiltin(name string) {
	b, ok := builtinWebhooks[name]
	if !ok {
		return
	}
	for _, f := range webhookFields {
		if *h.field(f) == "" {
			*h.field(f) = *b.field(f)
		}
	}
	if h.Each == "" {
		h.Each = b.Each
	}
	h.sign = b.sign
}

func builtinNames() []string {
	var out []string
	for n := range builtinWebhooks {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// renderWebhook turns one posted body into what it would publish on topic:
// every item rendered before any is published, so a bad item publishes nothing.
// The preview route runs it too, so a dry run matches a real post.
func renderWebhook(r *http.Request, topic string, h *Webhook, body []byte, hdr http.Header) ([]PublishInput, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // ids past 2^53 stay exact
	var data any
	if err := dec.Decode(&data); err != nil {
		return nil, errf(400, "bad_request", "bad JSON body: %v", err)
	}
	items := []any{data}
	if h.Each != "" {
		v := walk(data, h.Each)
		if v != nil {
			var ok bool
			if items, ok = v.([]any); !ok {
				return nil, errf(400, "bad_request", "%s is not a list", h.Each)
			}
			if len(items) > webhookItemsMax {
				return nil, errf(400, "bad_request", "%s has over %d items", h.Each, webhookItemsMax)
			}
		} else {
			items = nil
		}
	}
	t, err := h.parse(hdr)
	if err != nil {
		return nil, err
	}
	prio, err := ntfyPriority(param(r, "Priority", "Prio", "P")) // the URL's priority wins over the template's
	if err != nil {
		return nil, err
	}
	md := ntfyMarkdown(param(r, "Markdown", "Md")) // ntfy's flag, as on a plain publish
	ps := []PublishInput{}
	for _, item := range items {
		out := map[string]string{}
		for _, f := range webhookFields {
			if out[f], err = render(t, f, item); err != nil {
				return nil, err
			}
		}
		p := PublishInput{Topic: topic, ExternalID: out["id"], Title: out["title"], Message: out["message"],
			Click: out["click"], Priority: prio, Markdown: md, Tags: append(splitList(out["tags"]), splitList(param(r, "Tags", "Tag", "Ta"))...)}
		if p.Priority == 0 {
			if p.Priority, err = ntfyPriority(out["priority"]); err != nil {
				return nil, err
			}
		}
		if p.Priority == 0 {
			p.Priority = 3 // as Publish sets it, so a preview shows it
		}
		if out["resolve"] == "true" {
			if p.ExternalID == "" {
				continue // nothing to resolve by
			}
			p.Status = "resolved"
		}
		ps = append(ps, p)
	}
	return ps, nil
}

// webhookIn: POST /{topic}?template=<name>. Each item becomes one
// notification; one that renders resolve=true resolves the open row with its
// id instead (nothing to do if there is none, so a repeat delivery is fine).
func (s *Server) webhookIn(w http.ResponseWriter, r *http.Request, tok *Token, name string) error {
	topic := r.PathValue("topic")
	if err := validTopic(topic); err != nil {
		return err
	}
	if err := tok.need(ScopePublish, topic); err != nil { // before the lookup: no probing other topics' templates
		return err
	}
	h, err := s.webhook(r.Context(), topic, name)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBody))
	if err != nil {
		return errf(413, "too_large", "request body over %d bytes", maxBody)
	}
	if h.Secret != "" {
		check := h.sign
		if check == nil {
			check = sharedSecret
		}
		if !check(r, body, h.Secret) {
			return errf(401, "unauthorized", "webhook signature or secret does not match")
		}
	}
	ps, err := renderWebhook(r, topic, h, body, r.Header)
	if err != nil {
		return err
	}
	var created, resolved int
	for _, p := range ps { // ponytail: a 429 mid-batch keeps the ones before it; senders give external ids, so a retry upserts
		_, err := s.Publish(r.Context(), tok, p)
		switch {
		case p.Status == "resolved" && isNotFound(err): // already cleared
		case err != nil:
			return err
		case p.Status == "resolved":
			resolved++
		default:
			created++
		}
	}
	writeJSON(w, 200, map[string]int{"published": created, "resolved": resolved})
	return nil
}

// webhookRoutes: a topic's saved templates. Scope publish on the topic,
// since a template decides what gets published there.
func (s *Server) webhookRoutes() {
	owner := func(r *http.Request, tok *Token) (topic, name string, err error) {
		topic, name = r.PathValue("topic"), r.PathValue("name")
		if err = validTopic(topic); err != nil {
			return
		}
		if name != "" && !webhookNameRe.MatchString(name) {
			return "", "", errf(400, "bad_request", "invalid template name %q: use letters, digits, _ and -, at most 64", name)
		}
		return topic, name, tok.need(ScopePublish, topic)
	}
	s.api("GET /api/v1/topics/{topic}/templates", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic, _, err := owner(r, tok)
		if err != nil {
			return err
		}
		rows, err := s.db.QueryContext(r.Context(), `SELECT name, spec FROM webhook_templates WHERE topic = ? ORDER BY name`, topic)
		if err != nil {
			return err
		}
		defer rows.Close()
		type saved struct {
			Name string `json:"name"`
			Webhook
			HasSecret bool `json:"has_secret"`
		}
		out := []saved{}
		for rows.Next() {
			var v saved
			var spec string
			if err := rows.Scan(&v.Name, &spec); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(spec), &v.Webhook); err != nil {
				return err
			}
			v.HasSecret, v.Secret = v.Secret != "", ""
			out = append(out, v)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"webhooks": out, "builtin": builtinNames()})
		return nil
	})
	s.api("PUT /api/v1/topics/{topic}/templates/{name}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic, name, err := owner(r, tok)
		if err != nil {
			return err
		}
		var h Webhook
		if err := decodeJSON(r, &h); err != nil {
			return err
		}
		if _, err := h.parse(nil); err != nil {
			return err
		}
		if _, builtin := builtinWebhooks[name]; !builtin && h.Title == "" && h.Message == "" {
			return errf(400, "bad_request", "a template needs a title or a message")
		}
		spec, _ := json.Marshal(h)
		if _, err := s.db.ExecContext(r.Context(), `INSERT INTO webhook_templates (topic, name, spec) VALUES (?, ?, ?)
			ON CONFLICT(topic, name) DO UPDATE SET spec = excluded.spec`, topic, name, string(spec)); err != nil {
			return err
		}
		h.Secret = ""
		writeJSON(w, 200, h)
		return nil
	})
	// preview: what a post of payload would publish, stored and sent nowhere.
	// template, if given, is an unsaved one (over the built-in of that name);
	// headers stand in for the service's. No secret or signature check.
	s.api("POST /api/v1/topics/{topic}/templates/{name}/preview", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic, name, err := owner(r, tok)
		if err != nil {
			return err
		}
		var in struct {
			Payload  json.RawMessage   `json:"payload"`
			Headers  map[string]string `json:"headers"`
			Template *Webhook          `json:"template"`
		}
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
		if len(in.Payload) == 0 {
			return errf(400, "bad_request", "payload is required: the JSON a service would post")
		}
		h := in.Template
		if h != nil {
			h.overBuiltin(name)
		} else if h, err = s.webhook(r.Context(), topic, name); err != nil {
			return err
		}
		hdr := http.Header{}
		for k, v := range in.Headers {
			hdr.Set(k, v)
		}
		ps, err := renderWebhook(r, topic, h, in.Payload, hdr)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"notifications": ps})
		return nil
	})
	s.api("DELETE /api/v1/topics/{topic}/templates/{name}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		topic, name, err := owner(r, tok)
		if err != nil {
			return err
		}
		res, err := s.db.ExecContext(r.Context(), `DELETE FROM webhook_templates WHERE topic = ? AND name = ?`, topic, name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errf(404, "not_found", "no saved template %q on topic %q", name, topic)
		}
		writeJSON(w, 200, map[string]bool{"deleted": true})
		return nil
	})
}

// alerts is Alertmanager's (and Grafana's) alerts[]: one notification per
// alert, keyed by fingerprint, so a resolved alert resolves its own row.
var alerts = Webhook{
	Each:    "alerts",
	Title:   `{{get . "labels.alertname"}}`,
	Message: `{{get . "annotations.summary" "annotations.description"}}`,
	Click:   `{{get . "generatorURL"}}`,
	ID:      `{{get . "fingerprint"}}`,
	Resolve: `{{eq (get . "status") "resolved"}}`,
}

// builtinWebhooks: ?template=<name> without saving anything. Each has a
// page under docs/integrations/; the new ones have a fixture under
// testdata/webhooks/ (alertmanager and grafana are tested in ntfy_test.go).
var builtinWebhooks = map[string]Webhook{
	"alertmanager": alerts,
	"grafana":      alerts,

	// https://docs.github.com/en/webhooks/webhook-events-and-payloads
	// An issue or pull request is one notification, resolved when closed.
	"github": {
		Title:    `{{get . "repository.full_name" "organization.login"}}: {{header "X-GitHub-Event"}} {{get . "action"}}`,
		Message:  `{{get . "issue.title" "pull_request.title" "workflow_run.display_title" "check_run.name" "release.name" "head_commit.message" "zen"}}`,
		Click:    `{{get . "comment.html_url" "issue.html_url" "pull_request.html_url" "workflow_run.html_url" "check_run.html_url" "release.html_url" "compare" "repository.html_url"}}`,
		ID:       `{{with get . "issue.number" "pull_request.number"}}{{get $ "repository.full_name"}}#{{.}}{{end}}`,
		Resolve:  `{{and (eq (get . "action") "closed") (ne (get . "issue.number" "pull_request.number") "")}}`,
		Priority: `{{if eq (get . "workflow_run.conclusion" "check_run.conclusion" "check_suite.conclusion") "failure"}}high{{end}}`,
		sign:     hubSignature("X-Hub-Signature-256"),
	},

	// https://docs.sentry.io/organization/integrations/integration-platform/webhooks/ (examples in getsentry/sentry-docs)
	// Issues, issue alerts and metric alerts; a resolved issue or alert resolves.
	"sentry": {
		Title:    `{{get . "data.issue.title" "data.event.title" "data.metric_alert.alert_rule.name" "data.description_title"}}`,
		Message:  `{{get . "data.issue.culprit" "data.event.culprit" "data.description_text" "data.triggered_rule"}}`,
		Click:    `{{get . "data.issue.web_url" "data.event.web_url" "data.web_url"}}`,
		ID:       `{{with get . "data.issue.id" "data.event.issue_id"}}sentry-{{.}}{{else}}{{with get $ "data.metric_alert.id"}}sentry-metric-{{.}}{{end}}{{end}}`,
		Resolve:  `{{eq (get . "action") "resolved"}}`,
		Priority: `{{if eq (get . "action") "critical"}}urgent{{else if eq (get . "action") "warning"}}high{{end}}`,
		sign:     sentrySignature,
	},

	// https://docs.stripe.com/webhooks: one notification per event.
	"stripe": {
		Title:    `{{get . "type"}}`,
		Message:  `{{get . "data.object.object"}} {{get . "data.object.id"}}`,
		Click:    `https://dashboard.stripe.com/{{if eq (get . "livemode") "false"}}test/{{end}}events/{{get . "id"}}`,
		ID:       `{{with get . "id"}}stripe-{{.}}{{end}}`, // a retried delivery updates, not duplicates
		Priority: `{{if or (contains (get . "type") "fail") (contains (get . "type") "dispute")}}high{{end}}`,
		sign:     stripeSignature,
	},

	// https://developer.atlassian.com/cloud/jira/platform/webhooks/
	// One notification per issue, resolved when it is done or deleted.
	"jira": {
		Title:   `{{get . "issue.key"}}: {{get . "issue.fields.summary"}}`,
		Message: `{{with get . "issue.fields.status.name"}}{{.}}, {{end}}{{get . "user.displayName"}}`,
		Click:   `{{with get . "issue.self"}}{{index (split . "/rest/") 0}}/browse/{{get $ "issue.key"}}{{end}}`,
		ID:      `{{with get . "issue.key"}}jira-{{.}}{{end}}`,
		Resolve: `{{or (eq (get . "issue.fields.status.statusCategory.key") "done") (eq (get . "webhookEvent") "jira:issue_deleted")}}`,
		sign:    hubSignature("X-Hub-Signature"),
	},

	// Healthchecks.io: the body docs/integrations/healthchecks.md says to paste.
	"healthchecks": {
		Title:    `{{get . "name"}} is {{get . "status"}}`,
		ID:       `{{with get . "name"}}healthchecks-{{.}}{{end}}`,
		Resolve:  `{{eq (get . "status") "up"}}`,
		Priority: `{{if eq (get . "status") "down"}}high{{end}}`,
	},

	// Proxmox VE webhook target: the body docs/integrations/proxmox.md gives.
	"proxmox": {
		Title:    `{{get . "title"}}`,
		Message:  `{{get . "message"}}`,
		Priority: `{{with get . "severity"}}{{if eq . "error"}}high{{else if eq . "info"}}low{{end}}{{end}}`,
	},

	// Zabbix: the media type script in docs/integrations/zabbix.md. A
	// recovery (value 0) resolves the problem with the same event id.
	"zabbix": {
		Title:    `{{get . "subject"}}`,
		Message:  `{{get . "message"}}`,
		ID:       `{{with get . "eventid"}}zabbix-{{.}}{{end}}`,
		Resolve:  `{{eq (get . "value") "0"}}`,
		Priority: `{{with get . "severity"}}{{if eq . "5"}}urgent{{else if eq . "4"}}high{{else if or (eq . "0") (eq . "1")}}low{{end}}{{end}}`,
	},

	// https://learn.netdata.cloud/ (Cloud webhook): alerts and reachability.
	"netdata": {
		Title:    `{{get . "alert" "host"}}{{with get . "chart"}} on {{.}}{{end}}`,
		Message:  `{{get . "message" "info"}}`,
		Click:    `{{get . "alert_url" "url"}}`,
		ID:       `{{with get . "alert"}}netdata-{{.}}-{{get $ "chart"}}{{else}}{{with get $ "host"}}netdata-reach-{{.}}{{end}}{{end}}`,
		Resolve:  `{{or (eq (get . "severity") "clear") (eq (get . "status.reachable") "true")}}`,
		Priority: `{{if eq (get . "severity") "critical"}}high{{end}}`,
	},

	// Jellyfin Webhook plugin: the body docs/integrations/jellyfin.md gives.
	"jellyfin": {
		Title:   `{{get . "name" "type"}}`,
		Message: `{{get . "type"}}{{with get . "server"}} on {{.}}{{end}}{{with get . "user"}} by {{.}}{{end}}`,
		Click:   `{{get . "url"}}`,
	},

	// Sonarr, Radarr, Lidarr and Readarr's own webhook connection. A health
	// warning is one notification per check, resolved by HealthRestored.
	"arr": {
		Title:    `{{get . "instanceName"}}: {{get . "eventType"}}`,
		Message:  `{{get . "message" "series.title" "movie.title" "artist.name" "author.name"}}`,
		Click:    `{{get . "applicationUrl"}}`,
		ID:       `{{if contains (get . "eventType") "Health"}}arr-{{get . "instanceName"}}-{{get . "type"}}{{end}}`,
		Resolve:  `{{eq (get . "eventType") "HealthRestored"}}`,
		Priority: `{{if or (eq (get . "level") "error") (eq (get . "eventType") "ManualInteractionRequired")}}high{{end}}`,
	},
}
