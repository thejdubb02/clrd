package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/thejdubb02/clrd/server"
	"github.com/thejdubb02/clrd/web"
)

// mountWeb serves the landing page, docs and inbox from the same binary.
func mountWeb(s *server.Server, baseURL string) {
	h := signInLink(s, web.Handler(webAPI{s}, baseURL))
	for _, p := range []string{"/", "/docs", "/app", "/static", "/assets", "/favicon.svg", "/robots.txt", "/sitemap.xml"} {
		s.Mount(p, h)
	}
}

// signInLink trades /app?auth=... for the session cookie and a redirect to the
// same page without the token, so it leaves the address bar and history and is
// never sent as a referrer. The server's own sign-in handler sets the cookie or,
// for a bad token, sends the browser back with error=token. A good link replaces
// any session this browser already had, so a link followed from another site
// (Sec-Fetch-Site cross-site or same-site) only drops the token: otherwise any
// page could sign a visitor into someone else's inbox.
func signInLink(s *server.Server, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.URL.Query().Get("auth")
		if a == "" || r.Method != http.MethodGet || (r.URL.Path != "/app" && !strings.HasPrefix(r.URL.Path, "/app/")) {
			h.ServeHTTP(w, r)
			return
		}
		next := *r.URL
		q := next.Query()
		q.Del("auth")
		next.RawQuery = q.Encode()
		w.Header().Set("Referrer-Policy", "no-referrer")
		if f := r.Header.Get("Sec-Fetch-Site"); f == "cross-site" || f == "same-site" {
			http.Redirect(w, r, next.RequestURI(), http.StatusSeeOther)
			return
		}
		form := url.Values{"token": {linkToken(a)}, "next": {next.RequestURI()}}
		r2, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, "/api/v1/session", strings.NewReader(form.Encode()))
		r2.Header = r.Header.Clone() // Origin, X-Forwarded-Proto: the cookie gets the same checks and flags as a form sign-in
		r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r2.Host, r2.TLS, r2.RemoteAddr = r.Host, r.TLS, r.RemoteAddr
		s.ServeHTTP(w, r2)
	})
}

// linkToken reads ?auth= the way the server does for ntfy clients: base64 of a
// Bearer or Basic Authorization header.
// ponytail: copies server.rawToken's decoding; use a core helper once there is one.
func linkToken(a string) string {
	h := ""
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		if b, err := enc.DecodeString(a); err == nil {
			h = string(b)
			break
		}
	}
	scheme, v, _ := strings.Cut(h, " ")
	if strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(v)
	}
	if u, p, ok := (&http.Request{Header: http.Header{"Authorization": {"Basic " + v}}}).BasicAuth(); ok && strings.EqualFold(scheme, "Basic") {
		if strings.HasPrefix(p, "clrd_") {
			return p
		}
		return u
	}
	return ""
}

// webAPI reads the inbox for the signed-in browser straight from the store.
type webAPI struct{ s *server.Server }

// ponytail: up to 500 rows per state, so counts cap at 500; add a COUNT query if an inbox grows past that.
const webLimit = 500

func (a webAPI) Snapshot(r *http.Request, q web.Query) (web.Snapshot, error) {
	tok := a.s.Authenticate(r)
	if tok == nil {
		return web.Snapshot{}, web.ErrUnauthorized
	}
	ctx := r.Context()
	snap := web.Snapshot{Seq: a.s.CurrentSeq(), Counts: map[string]int{}, Me: tok.Name} // seq first, so replay cannot skip an event
	want := map[string]bool{}
	for _, st := range q.States {
		want[st] = true
	}
	var topics []string
	if q.Topic != "" {
		topics = []string{q.Topic}
	}
	for _, st := range []string{"active", "acknowledged", "snoozed", "dismissed", "resolved"} {
		rows, err := a.s.List(ctx, tok, server.ListQuery{State: st, Limit: webLimit})
		if err != nil {
			return snap, err
		}
		snap.Counts[st] = len(rows)
		if !want[st] {
			continue
		}
		if q.Topic != "" || q.Q != "" {
			if rows, err = a.s.List(ctx, tok, server.ListQuery{State: st, Topics: topics, Q: q.Q, Limit: webLimit}); err != nil {
				return snap, err
			}
		}
		var items []web.Notification
		if err := convert(rows, &items); err != nil {
			return snap, err
		}
		snap.Items = append(snap.Items, items...)
	}
	sort.Slice(snap.Items, func(i, j int) bool { return snap.Items[i].CreatedAt.After(snap.Items[j].CreatedAt) })
	tc, err := a.s.Topics(ctx, tok)
	if err != nil {
		return snap, err
	}
	for _, t := range tc {
		snap.Topics = append(snap.Topics, web.Topic{Name: t.Topic, Open: t.Open})
	}
	if tok.Has("admin") { // device names are the owner's business, not every reader's
		toks, err := a.s.Tokens(ctx, time.Now().Add(-10*time.Minute))
		if err != nil {
			return snap, err
		}
		for _, t := range toks {
			if t.LastSeenAt != nil {
				snap.Devices = append(snap.Devices, web.Device{Name: t.Name, LastSeen: *t.LastSeenAt})
			}
		}
	}
	return snap, nil
}

// convert copies through JSON; both types follow the wire format in docs/SPEC.md.
func convert(from, to any) error {
	b, err := json.Marshal(from)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, to)
}

func (a webAPI) Act(r *http.Request, id, action string, until *time.Time) error {
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	_, err := a.s.Act(r.Context(), tok, id, action, until)
	return err
}
