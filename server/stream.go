package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// hub fans committed events out to live subscribers.
type hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func (h *hub) subscribe() chan Event {
	ch := make(chan Event, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}

// publish never blocks: a subscriber whose buffer is full is dropped (its
// channel closes), so it reconnects and replays from its last seq.
func (h *hub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Subscribe returns every event committed from now on, and a cancel func.
// The channel closes if the reader falls behind. For mounted handlers
// (MCP wait_for_reply).
func (s *Server) Subscribe() (<-chan Event, func()) {
	ch := s.hub.subscribe()
	return ch, func() { s.hub.unsubscribe(ch) }
}

var (
	msgReset   = []byte(`{"type":"reset"}`)
	errDropped = errors.New("fell behind the live stream")
	errReset   = errf(410, "reset", "since is outside the event log: fetch the open list again")
	errRevoked = errf(401, "unauthorized", "token revoked")
)

func msgReady(seq int64) []byte { return fmt.Appendf(nil, `{"type":"ready","seq":%d}`, seq) }

func visible(tok *Token, topics []string, topic string) bool {
	return tok.Allows(topic) && (len(topics) == 0 || slices.Contains(topics, topic))
}

// follow is the sync loop every stream shares: replay events after since,
// send ready, then live events, pinging every 30s, until ctx ends or the
// subscriber is dropped. send gets each message and its seq (0 for ready/reset).
func (s *Server) follow(ctx context.Context, tok *Token, since int64, topics []string,
	send func(msg []byte, seq int64) error, ping func() error) error {
	sub := s.hub.subscribe() // first, so nothing committed during replay is missed
	defer s.hub.unsubscribe(sub)
	inner := send
	send = func(msg []byte, seq int64) error {
		if _, gone := s.revoked.Load(tok.ID); gone {
			return errRevoked
		}
		return inner(msg, seq)
	}
	last := s.seq.Load()
	if since < s.pruned.Load() || since > last {
		if err := send(msgReset, 0); err != nil {
			return err
		}
		since = last
	}
	for {
		evs, err := s.eventsAfter(ctx, since, 500)
		if err == errReset { // pruned under us mid-replay
			if err := send(msgReset, 0); err != nil {
				return err
			}
			since = max(since, s.seq.Load())
			continue
		}
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			break
		}
		for _, e := range evs {
			if visible(tok, topics, e.Notification.Topic) {
				if err := send(e.Raw, e.Seq); err != nil {
					return err
				}
			}
			since = e.Seq
		}
	}
	last = max(last, since)
	if err := send(msgReady(last), 0); err != nil {
		return err
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if !s.tokenAlive(ctx, tok) {
				return errRevoked
			}
			if err := ping(); err != nil {
				return err
			}
		case e, ok := <-sub:
			if !ok {
				return errDropped
			}
			if e.Seq <= last {
				continue // already sent during replay
			}
			last = e.Seq
			if visible(tok, topics, e.Notification.Topic) {
				if err := send(e.Raw, e.Seq); err != nil {
					return err
				}
			}
		}
	}
}

// tokenAlive notes the device as seen and reports whether its token still exists.
func (s *Server) tokenAlive(ctx context.Context, tok *Token) bool {
	var one int
	if s.db.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE id = ?`, tok.ID).Scan(&one) != nil {
		return false
	}
	s.touch(tok)
	return true
}

func sinceParam(r *http.Request) int64 {
	v := r.URL.Query().Get("since")
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		v = id
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request, tok *Token) error {
	if err := tok.need(ScopeRead, ""); err != nil {
		return err
	}
	// Accept refuses cross-origin browsers by default, which keeps the
	// cookie from being usable by other sites.
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return nil // Accept already wrote the response
	}
	defer c.CloseNow()
	ctx := c.CloseRead(r.Context())
	err = s.follow(ctx, tok, sinceParam(r), splitList(r.URL.Query().Get("topics")),
		func(msg []byte, _ int64) error {
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return c.Write(wctx, websocket.MessageText, msg)
		},
		func() error {
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return c.Ping(pctx)
		})
	if err != nil && ctx.Err() == nil {
		c.Close(websocket.StatusTryAgainLater, err.Error())
	}
	return nil
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request, tok *Token) error {
	if err := tok.need(ScopeRead, ""); err != nil {
		return err
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	write := func(format string, args ...any) error {
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return err
		}
		return rc.Flush()
	}
	s.follow(r.Context(), tok, sinceParam(r), splitList(r.URL.Query().Get("topics")),
		func(msg []byte, seq int64) error {
			if seq > 0 {
				return write("id: %d\ndata: %s\n\n", seq, msg)
			}
			return write("data: %s\n\n", msg)
		},
		func() error { return write(": ping\n\n") })
	return nil
}

// handleEvents is the polling form: events after since, and the seq to ask from next.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, tok *Token) error {
	if err := tok.need(ScopeRead, ""); err != nil {
		return err
	}
	since := sinceParam(r)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	cur := s.CurrentSeq()
	if since < s.pruned.Load() || since > cur {
		return errReset
	}
	evs, err := s.eventsAfter(r.Context(), since, limit)
	if err != nil {
		return err
	}
	topics := splitList(r.URL.Query().Get("topics"))
	out := []json.RawMessage{}
	next := since
	for _, e := range evs {
		if visible(tok, topics, e.Notification.Topic) {
			out = append(out, e.Raw)
		}
		next = e.Seq
	}
	if len(evs) < limit {
		next = max(next, cur) // the log is complete up to cur
	}
	writeJSON(w, 200, map[string]any{"events": out, "seq": next})
	return nil
}
