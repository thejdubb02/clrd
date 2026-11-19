package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/thejdubb02/clrd/server"
	"github.com/thejdubb02/clrd/web"
)

// mountWeb serves the landing page, docs and inbox from the same binary.
func mountWeb(s *server.Server, baseURL string) {
	h := web.Handler(webAPI{s}, baseURL)
	for _, p := range []string{"/", "/docs", "/app", "/static", "/assets", "/favicon.svg", "/robots.txt", "/sitemap.xml"} {
		s.Mount(p, h)
	}
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
	snap := web.Snapshot{Seq: a.s.CurrentSeq(), Counts: map[string]int{}} // seq first, so replay cannot skip an event
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
