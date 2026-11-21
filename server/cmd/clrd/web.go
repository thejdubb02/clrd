package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/thejdubb02/clrd/server"
	"github.com/thejdubb02/clrd/web"
)

// mountWeb serves the landing page, docs and inbox from the same binary.
// With hide, only the inbox is served and nothing asks to be indexed.
func mountWeb(s *server.Server, baseURL string, hide bool) {
	h := signInLink(s, web.Handler(webAPI{s}, baseURL))
	if hide {
		h = private(s, h)
	}
	for _, p := range []string{"/", "/docs", "/app", "/static", "/assets", "/favicon.svg", "/favicon.ico", "/apple-touch-icon.png", "/manifest.webmanifest", "/robots.txt", "/sitemap.xml"} {
		s.Mount(p, h)
	}
}

// private hides the public site of a private server: / goes to the inbox,
// /docs and the sitemap are gone, and robots.txt turns every crawler away.
func private(s *server.Server, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		switch p := r.URL.Path; {
		case p == "/":
			http.Redirect(w, r, s.Path("/app"), http.StatusFound)
		case p == "/robots.txt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "User-agent: *\nDisallow: /\n")
		case p == "/docs" || strings.HasPrefix(p, "/docs/") || p == "/sitemap.xml":
			http.NotFound(w, r)
		default:
			h.ServeHTTP(w, r)
		}
	})
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
			http.Redirect(w, r, s.Path(next.RequestURI()), http.StatusSeeOther)
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

// ponytail: the page lists up to 500 rows per state (the counts are exact); add paging if an inbox needs more on screen.
const webLimit = 500

func (a webAPI) Snapshot(r *http.Request, q web.Query) (web.Snapshot, error) {
	tok := a.s.Authenticate(r)
	if tok == nil {
		return web.Snapshot{}, web.ErrUnauthorized
	}
	ctx := r.Context()
	snap := web.Snapshot{Seq: a.s.CurrentSeq(), Epoch: a.s.Epoch(), Me: tok.Name, MeID: tok.ID} // seq first, so replay cannot skip an event
	counts, err := a.s.Counts(ctx, tok)
	if err != nil {
		return snap, err
	}
	snap.Counts = counts
	ds, err := a.s.Settings(ctx, tok)
	if err != nil {
		return snap, err
	}
	snap.SnoozeIntervals, snap.SnoozeDefault = ds.SnoozeIntervals, ds.SnoozeDefault
	snap.KeepClearedDays = *ds.KeepClearedDays // Settings fills it from CLRD_RETENTION when unset
	snap.TopicKeepClearedDays = ds.TopicKeepClearedDays
	snap.Formatting, snap.Autolink, snap.Favicons = ds.RenderFormatting, ds.Autolink, ds.Favicons
	snap.LinkPreviews, snap.TopicPreviews = ds.LinkPreviews, ds.TopicPreviews
	snap.Sound = ds.Sound
	want := map[string]bool{}
	for _, st := range q.States {
		want[st] = true
	}
	var topics []string
	if q.Topic != "" {
		topics = []string{q.Topic}
	}
	for _, st := range []string{"active", "acknowledged", "snoozed", "dismissed", "resolved"} {
		if !want[st] {
			continue
		}
		rows, err := a.s.List(ctx, tok, server.ListQuery{State: st, Topics: topics, Q: q.Q, Limit: webLimit})
		if err != nil {
			return snap, err
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
		snap.Topics = append(snap.Topics, web.Topic{Name: t.Topic, Open: t.Open, Icon: t.Icon})
	}
	// Every page's sync indicator (#295). An admin sees every device; anyone
	// else only their own (#245), so a reader never learns other people's devices.
	devs, err := a.s.Devices(ctx, tok)
	if err != nil {
		return snap, err
	}
	if err := convert(devs, &snap.Linked); err != nil {
		return snap, err
	}
	if q.Devices {
		evs, err := a.s.DeviceLog(ctx, tok)
		if err != nil {
			return snap, err
		}
		if err := convert(evs, &snap.Log); err != nil {
			return snap, err
		}
		// The format check is advice: if it fails, Settings still loads without it.
		if fc, err := a.s.FormatCheck(r, tok, ""); err == nil {
			_ = convert(fc["topics"], &snap.Format)
		}
		// A token without the read scope sees no silences, but still gets Settings.
		if sils, err := a.s.Silences(ctx, tok); err == nil {
			_ = convert(sils, &snap.Silences)
			for i := range snap.Silences {
				snap.Silences[i].CanEnd = tok.MaySilence(sils[i].Topic)
			}
		}
		snap.CanSilence = tok.Has(server.ScopePublish)
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
	_, err := a.s.Act(r.Context(), tok, id, action, "web_form", until) // the inbox's no-script form buttons
	return err
}

// webError keeps only what an API client would see, so the page never shows
// an internal error's text.
func webError(err error) error {
	if err == nil {
		return nil
	}
	_, msg := server.PublicError(err)
	return errors.New(msg)
}

func (a webAPI) StartSilence(r *http.Request, topic, tag, dur, comment string) error {
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	_, err := a.s.StartSilence(r.Context(), tok, server.SilenceStart{Topic: topic, Tag: tag, For: dur, Comment: comment})
	return webError(err)
}

func (a webAPI) EndSilence(r *http.Request, id string) error {
	tok := a.s.Authenticate(r)
	if tok == nil {
		return web.ErrUnauthorized
	}
	err := a.s.EndSilence(r.Context(), tok, id)
	if status, _ := server.PublicError(err); status == 404 {
		return nil // it already ran out: the list was just stale
	}
	return webError(err)
}

func (a webAPI) Reply(r *http.Request, id, reply string, seq int64) error {
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	_, err := a.s.Reply(r.Context(), tok, id, reply, "web_form", seq)
	return err
}
