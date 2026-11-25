package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// hub fans committed events out to live subscribers, each keyed by the
// token it streams for so a revoke can cut that token's streams at once.
type hub struct {
	mu   sync.Mutex
	subs map[chan Event]*stream
}

// stream is one live subscriber's sync state, for the device list (#300).
type stream struct {
	tok   string
	live  bool         // takes live-only messages (#400): ntfy streams only
	ready atomic.Bool  // replay done and ready sent: caught up
	seq   atomic.Int64 // the last event seq the server sent it, or passed over as not visible to it
	cut   atomic.Bool  // dropped for an access change: send nothing more, not even mid-replay (#428)
}

func (h *hub) subscribe(tokenID string, live bool) (chan Event, *stream) {
	ch, st := make(chan Event, 256), &stream{tok: tokenID, live: live}
	h.mu.Lock()
	h.subs[ch] = st
	h.mu.Unlock()
	return ch, st
}

// status reports whether tokenID has a live stream, whether one of them is
// caught up, and the highest seq sent to any of them.
func (h *hub) status(tokenID string) (connected, caughtUp bool, seq int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, st := range h.subs {
		if st.tok == tokenID {
			connected = true
			caughtUp = caughtUp || st.ready.Load()
			seq = max(seq, st.seq.Load())
		}
	}
	return
}

// drop closes every subscription of one token.
func (h *hub) drop(tokenID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, st := range h.subs {
		if st.tok == tokenID {
			st.cut.Store(true)
			delete(h.subs, ch)
			close(ch)
		}
	}
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
	for ch, st := range h.subs {
		if e.Notification.Live && !st.live {
			continue
		}
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
	ch, _ := s.hub.subscribe("", false)
	return ch, func() { s.hub.unsubscribe(ch) }
}

var (
	msgReset   = []byte(`{"type":"reset"}`)
	errDropped = errors.New("fell behind the live stream")
	errReset   = errf(410, "reset", "since is outside the event log: fetch the open list again")
	errRevoked = errf(401, "unauthorized", "token revoked")
)

// msgReady ends the replay. mark_id is the by_id this token's marks get, so a
// client can tell its own mark on a card (docs/SPEC.md section 2, "Marks").
func msgReady(seq int64, epoch, markID string) []byte {
	return fmt.Appendf(nil, `{"type":"ready","seq":%d,"epoch":%q,"mark_id":%q}`, seq, epoch, markID)
}

// visible reports whether tok gets an event on topic. A subscriptions event
// is refused first, as may would pass it for a token that reads every topic.
func visible(tok *Token, topics []string, topic string) bool {
	return !strings.HasPrefix(topic, subMark) && tok.may(ScopeRead, topic) && !tok.view.hides(topic) &&
		(len(topics) == 0 || slices.Contains(topics, topic))
}

// notChosen reports one sent to chosen devices (#82) that tok is not among.
// It still reaches tok, but silenced, so only the chosen ones alert.
func notChosen(tok *Token, n *Notification) bool {
	return len(n.Devices) > 0 && !slices.Contains(n.Devices, tok.ID)
}

// forReader silences each of ns that tok was not chosen for, as the stream
// does, so a client rebuilding from a list gets the same answer (#588).
func forReader(tok *Token, ns ...*Notification) {
	for _, n := range ns {
		if notChosen(tok, n) {
			n.Silenced = true
		}
	}
}

// rawFor is e as tok gets it (notChosen). Only this copy changes: the row,
// the log and webhooks keep what was stored.
func rawFor(tok *Token, e Event) []byte {
	if !notChosen(tok, e.Notification) {
		return e.Raw
	}
	var c Event
	if json.Unmarshal(e.Raw, &c) != nil || c.Notification == nil || c.Notification.Silenced {
		return e.Raw
	}
	c.Notification.Silenced = true
	if b, err := json.Marshal(c); err == nil {
		return b
	}
	return e.Raw
}

// follow is the sync loop every stream shares: replay events after since,
// send ready, then live events, pinging every 30s, until ctx ends or the
// subscriber is dropped. send gets each message and its seq (-1 for reset;
// ready carries the seq it is ready at, which can be 0). It is ntfy's: the
// stream also takes live-only messages, which never move the seq, and
// opened (if set) runs once it is subscribed, so none sent after is missed.
func (s *Server) follow(ctx context.Context, tok *Token, since int64, topics []string, opened func() error,
	send func(msg []byte, seq int64) error, ping func() error) error {
	return s.followWith(ctx, tok, since, topics, false, true, opened, send, ping)
}

// followWith is follow; withSettings also sends a settings message just
// before ready and again after every change (seq -1, so SSE sets no id).
// A tok with a view follows its owner's subscriptions (#617): the view is
// read again here, after subscribing, and on every change of that set.
func (s *Server) followWith(ctx context.Context, tok *Token, since int64, topics []string, withSettings, live bool,
	opened func() error, send func(msg []byte, seq int64) error, ping func() error) error {
	sub, st := s.hub.subscribe(tok.ID, live) // first, so nothing committed during replay is missed
	s.devicesChanged.fire()
	defer func() {
		s.hub.unsubscribe(sub)
		s.devicesChanged.fire() // after, so the others see this stream gone
	}()
	// Access as it is now: a grant change between sign-in and subscribe
	// dropped no stream of ours, and any later one drops this (#428).
	subs, follow := tok.view, tok.follow
	if tok = s.tokenWhere(ctx, `id = ?`, tok.ID); tok == nil {
		return errRevoked
	}
	tok.follow = follow              // the request's subscribed=mine, not stored on the row (#764)
	hid, err := s.hiddenOf(ctx, tok) // after subscribing, so a hide since is in the set or still to come
	if err != nil {
		return err
	}
	// resync reads the view and returns a seq it covers: the head before the
	// read, or the view's own change if newer. Everything up to it is in what
	// a client reads after a reset, so the stream goes on from there, never
	// filtering an earlier event by a later set, nor a later one by an older.
	resync := func() (int64, error) {
		head := s.seq.Load()
		h, err := s.hiddenOf(ctx, tok) // a hide up to head is skipped with the rest, so read it here (#767)
		if err != nil {
			return 0, err
		}
		hid = h
		if subs == nil {
			return head, nil
		}
		t, err := s.withView(ctx, tok, subs.all)
		if err != nil {
			return 0, err
		}
		tok = t
		return max(head, tok.view.seq), nil
	}
	inner := send
	send = func(msg []byte, seq int64) error {
		if _, gone := s.revoked.Load(tok.ID); gone {
			return errRevoked
		}
		if st.cut.Load() {
			return errDropped
		}
		return inner(msg, seq)
	}
	if opened != nil {
		if err := opened(); err != nil {
			return err
		}
	}
	// reset tells the client its since is gone and gives where to go on: to
	// (-1: the seq once sent), or, when send answers errPruned (ntfy has no
	// reset), the oldest event left. Either way on the same subscription, so
	// no live event is lost (#443).
	reset := func(to int64) (int64, error) {
		err := send(msgReset, -1)
		if to < 0 {
			to = max(since, s.seq.Load())
		}
		if err != errPruned {
			return to, err
		}
		var oldest int64
		err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(seq) - 1, ?) FROM events`, s.seq.Load()).Scan(&oldest)
		return min(max(since, oldest), s.seq.Load()), err
	}
	last, err := resync() // after subscribe and opened, so a change in between is in the view
	if err != nil {
		return err
	}
	if since < s.pruned.Load() || since > last || (tok.view != nil && since < tok.view.seq) {
		if since, err = reset(last); err != nil {
			return err
		}
	}
replay:
	for {
		if _, gone := s.revoked.Load(tok.ID); gone { // a long replay may send nothing visible
			return errRevoked
		}
		evs, err := s.eventsAfter(ctx, since, 500)
		if err == errReset { // pruned under us mid-replay: the view may be older than the reset too
			head, err := resync()
			if err != nil {
				return err
			}
			if since, err = reset(head); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if len(evs) == 0 {
			break
		}
		for _, e := range evs {
			if tok.view.changedBy(e) { // what was sent is stale: read the new set, reset past it
				if err := send(e.Raw, -1); err != nil { // no SSE id: only ready after the reset moves it
					return err
				}
				head, err := resync()
				if err != nil {
					return err
				}
				if since, err = reset(head); err != nil {
					return err
				}
				st.seq.Store(since)
				continue replay
			}
			if visible(tok, topics, e.Notification.Topic) && hid.passes(tok, e) {
				if err := send(rawFor(tok, e), e.Seq); err != nil {
					return err
				}
			}
			since = e.Seq
			st.seq.Store(since)
		}
	}
	var changed <-chan struct{} // nil blocks forever: no settings
	sendSettings := func() error {
		changed = s.settingsChanged.wait() // before reading, so no change is missed
		msg, err := s.settingsMsg(ctx, tok)
		if err != nil {
			return err
		}
		return send(msg, -1)
	}
	if withSettings {
		if err := sendSettings(); err != nil {
			return err
		}
	}
	last = max(last, since)
	st.seq.Store(last)
	st.ready.Store(true)
	s.devicesChanged.fire()
	var devChanged <-chan struct{} // nil blocks forever: no device updates
	var devSeen string
	if withSettings {
		// After our own fire, so a stream hears the others and not itself, and
		// before ready, so a client that reads the list on ready misses nothing.
		devChanged = s.devicesChanged.wait()
		_, key, err := s.devicesMsg(ctx, tok)
		if err != nil {
			return err
		}
		devSeen = key
	}
	markID, err := s.MarkAuthor(ctx, tok)
	if err != nil {
		return err
	}
	if err := send(msgReady(last, s.epoch, markID), last); err != nil { // seq too, so SSE moves Last-Event-ID past a reset
		return err
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
			if err := sendSettings(); err != nil {
				return err
			}
		case <-devChanged:
			devChanged = s.devicesChanged.wait() // before reading, so no change is missed
			msg, key, err := s.devicesMsg(ctx, tok)
			if err != nil {
				return err
			}
			if key == devSeen {
				continue // the change was outside this token's list
			}
			devSeen = key
			if err := send(msg, -1); err != nil {
				return err
			}
		case <-t.C:
			if !s.tokenAlive(ctx, tok) {
				return errRevoked
			}
			if err := ping(); err != nil {
				return err
			}
		case e, ok := <-sub:
			if !ok {
				if _, gone := s.revoked.Load(tok.ID); gone {
					return errRevoked
				}
				return errDropped
			}
			if e.Notification.Live { // never stored, so outside the seq order: last stays
				if visible(tok, topics, e.Notification.Topic) {
					if err := send(rawFor(tok, e), e.Seq); err != nil {
						return err
					}
				}
				continue
			}
			if e.Seq <= last {
				continue // already sent during replay
			}
			last = e.Seq
			if tok.view.changedBy(e) { // the set changed: new settings, then reset and ready (SPEC 2.3)
				if err := send(e.Raw, -1); err != nil { // no SSE id: only ready after the reset moves it
					return err
				}
				head, err := resync()
				if err != nil {
					return err
				}
				last = max(last, head) // events up to head are in the reload, never sent live
				st.seq.Store(last)
				if withSettings { // from the same view
					if err := sendSettings(); err != nil {
						return err
					}
				}
				if err := send(msgReset, -1); err != nil {
					return err
				}
				if err := send(msgReady(last, s.epoch, markID), last); err != nil {
					return err
				}
				continue
			}
			if visible(tok, topics, e.Notification.Topic) && hid.passes(tok, e) {
				if err := send(rawFor(tok, e), e.Seq); err != nil {
					return err
				}
			}
			st.seq.Store(last)
		}
	}
}

// tokenAlive notes the device as seen and reports whether its token still exists.
func (s *Server) tokenAlive(ctx context.Context, tok *Token) bool {
	var one int
	if s.db.QueryRowContext(ctx, `SELECT 1 FROM tokens WHERE id = ?`, tok.ID).Scan(&one) != nil {
		return false
	}
	s.touch(ctx, tok)
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

// followsSubs asks followWith to filter tok's stream by its owner's
// subscriptions (WebSocket and SSE; ntfy streams are never filtered).
// followWith loads the view itself, after it subscribes.
func followsSubs(r *http.Request, tok *Token) *Token {
	c := *tok
	c.view = &view{all: subscribedAll(r)}
	return &c
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
	err = s.followWith(ctx, followsSubs(r, tok), sinceParam(r), splitList(r.URL.Query().Get("topics")), true, false, nil,
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

// streamWriteTimeout bounds one write to an SSE or ntfy stream. Without it a
// client that stops reading holds the stream open on a blocked write, past a revoke.
var streamWriteTimeout = 10 * time.Second

// boundEnd bounds the write net/http does after the handler returns (the end of the chunked body).
func boundEnd(rc *http.ResponseController) { rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout)) }

func flushWrite(rc *http.ResponseController, w io.Writer, b []byte) error {
	rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
	defer rc.SetWriteDeadline(time.Time{})
	if _, err := w.Write(b); err != nil {
		return err
	}
	return rc.Flush()
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request, tok *Token) error {
	if err := tok.need(ScopeRead, ""); err != nil {
		return err
	}
	rc := http.NewResponseController(w)
	defer boundEnd(rc)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	write := func(format string, args ...any) error {
		return flushWrite(rc, w, fmt.Appendf(nil, format, args...))
	}
	s.followWith(r.Context(), followsSubs(r, tok), sinceParam(r), splitList(r.URL.Query().Get("topics")), true, false, nil,
		func(msg []byte, seq int64) error {
			if seq >= 0 { // ready too, so EventSource drops a Last-Event-ID from before a reset
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
	tok, err := s.withView(r.Context(), tok, subscribedAll(r)) // after cur, so a change after it shows up below
	if err != nil {
		return err
	}
	if since < s.pruned.Load() || since > cur || since < tok.view.seq {
		return errReset
	}
	evs, err := s.eventsAfter(r.Context(), since, limit)
	if err != nil {
		return err
	}
	hid, err := s.hiddenOf(r.Context(), tok)
	if err != nil {
		return err
	}
	topics := splitList(r.URL.Query().Get("topics"))
	out := []json.RawMessage{}
	next := since
	for _, e := range evs {
		if tok.view.changedBy(e) { // the set changed within this page: start over
			return errReset
		}
		if visible(tok, topics, e.Notification.Topic) && hid.passes(tok, e) {
			out = append(out, rawFor(tok, e))
		}
		next = e.Seq
	}
	if len(evs) < limit {
		next = max(next, cur) // the log is complete up to cur
	}
	writeJSON(w, 200, map[string]any{"events": out, "seq": next, "epoch": s.epoch})
	return nil
}
