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
	if snap.MarkID, err = a.s.MarkAuthor(ctx, tok); err != nil {
		return snap, err
	}
	ds, err := a.s.Settings(ctx, tok)
	if err != nil {
		return snap, err
	}
	snap.SnoozeIntervals, snap.SnoozeDefault = ds.SnoozeIntervals, ds.SnoozeDefault
	snap.SwipeStart, snap.SwipeEnd = ds.SwipeStart, ds.SwipeEnd
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
		snap.Topics = append(snap.Topics, web.Topic{Name: t.Topic, Open: t.Open, Icon: t.Icon, Silence: tok.MaySilence(t.Topic)})
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
	// Who may approve (#329): the server sets it on this token's own row (#433).
	for _, d := range devs {
		snap.CanApprove = snap.CanApprove || d.CanApprove
	}
	// Senders (#119): a full admin only, the same check as the API's.
	snap.CanSenders = tok.Has(server.ScopeAdmin) && len(tok.Topics) == 0 && len(tok.Grants) == 0
	if q.Senders && snap.CanSenders {
		ss, err := a.s.Senders(ctx, tok)
		if err != nil {
			return snap, err
		}
		if err := convert(ss, &snap.Senders); err != nil {
			return snap, err
		}
	}
	if q.Sender != "" && snap.CanSenders {
		// Changes before rules: an edit landing between the two reads then
		// makes the form's base_seq stale (refused), never its rules (#543).
		cs, err := a.s.SenderChanges(ctx, tok, q.Sender)
		if err != nil {
			return snap, err
		}
		sd, err := a.s.Sender(ctx, tok, q.Sender)
		if code, _ := server.PublicError(err); err != nil && code == http.StatusNotFound {
			return snap, web.ErrNotFound
		} else if err != nil {
			return snap, err
		}
		if err := convert(sd, &snap.Sender); err != nil {
			return snap, err
		}
		if err := convert(cs, &snap.Sender.Changes); err != nil {
			return snap, err
		}
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
		if fc, err := a.s.FormatCheck(r, tok, "", true); err == nil {
			_ = convert(fc["topics"], &snap.Format)
			_ = convert(fc["dismissed"], &snap.FormatDismissed)
			// Name only senders this token may see in Linked devices (#245):
			// anyone else's shows as another device, and its prompt, which
			// names the token, is left out.
			seen := map[string]string{} // ID to name
			for _, d := range snap.Linked {
				seen[d.ID] = d.Name
			}
			for i, g := range snap.Format {
				snap.Format[i].CanFix = tok.MaySilence(g.Topic) // the same publish check
				if g.SenderID != "" && seen[g.SenderID] == "" {
					snap.Format[i].Sender, snap.Format[i].Prompt = "", ""
				}
			}
			for i, d := range snap.FormatDismissed {
				snap.FormatDismissed[i].CanFix = tok.MaySilence(d.Topic)
				snap.FormatDismissed[i].Sender = seen[d.SenderID] // the server sends no names here
			}
			off, _ := fc["off"].([]string)
			for _, t := range off {
				snap.FormatOff = append(snap.FormatOff, web.FormatOff{Topic: t, CanFix: tok.MaySilence(t)})
			}
		}
		// A token without the read scope sees no silences, but still gets Settings.
		if sils, err := a.s.Silences(ctx, tok); err == nil {
			_ = convert(sils, &snap.Silences)
			for i := range snap.Silences {
				snap.Silences[i].CanEnd = tok.MaySilence(sils[i].Topic)
			}
		}
		// Offer the form only if some silence would be accepted: a topic list
		// with only read grants publishes nowhere.
		snap.CanInvite = a.s.MayInvite(ctx, tok)
		snap.CanSilence = tok.MaySilence("")
		for _, t := range snap.Topics {
			snap.CanSilence = snap.CanSilence || t.Silence
		}
		// Webhook templates (#129): publish on the topic, listed only where allowed.
		if tok.Has(server.ScopePublish) {
			tpls, err := a.s.Templates(ctx, tok, "")
			if err != nil {
				return snap, err
			}
			if err := convert(tpls, &snap.Templates); err != nil {
				return snap, err
			}
			snap.CanTemplates, snap.BuiltinTemplates = true, server.BuiltinTemplates()
		}
		// Webhooks are admin only (#238): anyone else gets no hook URLs at all.
		if hooks, err := a.s.OutHooks(ctx, tok, ""); err == nil {
			snap.CanHooks = true
			if err := convert(hooks, &snap.Hooks); err != nil {
				return snap, err
			}
			for i, h := range hooks {
				ds, err := a.s.HookDeliveries(ctx, tok, h.Topic, h.Name, 10)
				if err != nil {
					return snap, err
				}
				if err := convert(ds, &snap.Hooks[i].Deliveries); err != nil {
					return snap, err
				}
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

func (a webAPI) FormatIgnore(r *http.Request, topic, rule, senderID string, on bool) error {
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	return webError(a.s.SetFormatIgnore(r.Context(), tok, topic, rule, senderID, on))
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

func (a webAPI) SaveHook(r *http.Request, topic, name, url string, events []string, body *string, secret string, clearSecret bool) error {
	h := server.OutHook{URL: url, Events: events, Secret: secret}
	if body != nil {
		h.Body = *body
	}
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	return webError(a.s.SaveHook(r.Context(), tok, topic, name, h, !clearSecret, body == nil))
}

func (a webAPI) SaveTemplate(r *http.Request, t web.Template, secret string, clearSecret bool) error {
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	h := server.Webhook{Each: t.Each, Title: t.Title, Message: t.Message, Priority: t.Priority, Tags: t.Tags,
		Click: t.Click, ID: t.ID, Resolve: t.Resolve, Secret: secret}
	return webError(a.s.SaveTemplate(r.Context(), tok, t.Topic, t.Name, h, !clearSecret))
}

func (a webAPI) SetSenderRules(r *http.Request, id string, rules web.SenderRules, base int64) error {
	tok := a.s.Authenticate(r)
	if tok == nil {
		return web.ErrUnauthorized
	}
	patch, err := json.Marshal(struct {
		web.SenderRules
		BaseSeq int64 `json:"base_seq"`
	}{rules, base})
	if err != nil {
		return err
	}
	_, err = a.s.SetSenderRules(r.Context(), tok, id, patch)
	return webError(err)
}

func (a webAPI) UndoSenderChange(r *http.Request, id string, seq int64) error {
	tok := a.s.Authenticate(r)
	if tok == nil {
		return web.ErrUnauthorized
	}
	_, err := a.s.UndoSenderChange(r.Context(), tok, id, seq)
	return webError(err)
}

func (a webAPI) DeleteTemplate(r *http.Request, topic, name string) error {
	tok := a.s.Authenticate(r)
	if tok == nil {
		return web.ErrUnauthorized
	}
	err := a.s.DeleteTemplate(r.Context(), tok, topic, name)
	if status, _ := server.PublicError(err); status == 404 {
		return nil // already gone: the page was stale
	}
	return webError(err)
}

func (a webAPI) DeleteHook(r *http.Request, topic, name string) error {
	tok := a.s.Authenticate(r)
	if tok == nil {
		return web.ErrUnauthorized
	}
	err := a.s.DeleteHook(r.Context(), tok, topic, name)
	if status, _ := server.PublicError(err); status == 404 {
		return nil // already gone: the page was stale
	}
	return webError(err)
}

func (a webAPI) Reply(r *http.Request, id, reply, text string, seq int64) error {
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	_, err := a.s.Reply(r.Context(), tok, id, reply, text, "web_form", seq)
	return webError(err) // the page shows why: not allowed, its own request, or answered already
}

func (a webAPI) Mark(r *http.Request, id, mark, note, link string) error {
	tok := a.s.Authenticate(r) // checks Origin on a cookie POST
	if tok == nil {
		return web.ErrUnauthorized
	}
	_, err := a.s.SetMark(r.Context(), tok, id, mark, note, link)
	return webError(err) // the page shows why: a long note or a link that is not http
}
