package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
)

// Where a device's clears apply (#767). By default a dismiss clears the
// notification for everyone. A device with "clears apply everywhere" off
// hides it for itself alone: the shared state, the other devices, hooks and
// pushes never hear of it. The settings row holds the ids of those devices.
const localClearsKey = "local_clears"

func localClears(ctx context.Context, q querier) (map[string]bool, error) {
	m := map[string]bool{}
	return m, settingJSON(ctx, q, localClearsKey, &m)
}

// SetClears sets where device id's clears apply, under the rules of
// SetApprove: tok is allowed, not limited to some topics, and sees the device.
func (s *Server) SetClears(ctx context.Context, tok *Token, id string, everywhere bool) (LinkedDevice, error) {
	var d LinkedDevice
	if err := approveGate(tok, ApproveAllowed); err != nil {
		return d, err
	}
	tx, d, err := s.deviceTx(ctx, tok, id)
	if tx != nil {
		defer tx.Rollback()
	}
	var mine string
	if err == nil {
		mine, err = approveOf(ctx, tx, tok.ID)
	}
	if err == nil && mine != ApproveAllowed {
		err = errf(403, "approve_denied", "only an allowed device can change where clears apply")
	}
	var m map[string]bool
	if err == nil {
		m, err = localClears(ctx, tx)
	}
	if err == nil {
		if everywhere {
			delete(m, id)
		} else {
			m[id] = true
		}
		d.ClearsEverywhere = everywhere
		if err = putSettingJSON(ctx, tx, localClearsKey, m); err == nil {
			err = tx.Commit()
		}
	}
	s.mu.Unlock()
	if err == nil {
		s.devicesChanged.fire()
	}
	return d, err
}

// hiddenView is n as the device that hid it sees it: cleared by that device.
func hiddenView(tok *Token, n *Notification) *Notification {
	c := *n
	c.State, c.SnoozeUntil = Dismissed, nil
	c.setBy(tok)
	return &c
}

// isHidden reports whether tok hid n, which is still open for the others.
func isHidden(ctx context.Context, q querier, tok *Token, n *Notification) bool {
	var one int
	return !n.Final() && q.QueryRowContext(ctx, `SELECT 1 FROM hidden WHERE token_id = ? AND notification_id = ?`, tok.ID, n.ID).Scan(&one) == nil
}

// notHidden leaves out what the token (the one argument) hid while still open.
const notHidden = `NOT (state NOT IN ('dismissed', 'resolved') AND id IN (SELECT notification_id FROM hidden WHERE token_id = ?))`

// hide hides open n for tok alone and returns the event only tok gets, or
// nil when tok hid it already.
func hide(ctx context.Context, tx *sql.Tx, tok *Token, n *Notification, via string) (*Event, error) {
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO hidden (token_id, notification_id) VALUES (?, ?)`, tok.ID, n.ID)
	if err != nil {
		return nil, err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return nil, nil
	}
	return &Event{Type: EventState, Notification: hiddenView(tok, n), Via: via, Only: tok.ID}, nil
}

// clearLocal is act's dismiss or reopen when it stays on tok's device. It
// returns the notification as tok sees it and the events, or a nil
// notification when the shared change goes ahead as usual. A reopen of a
// card tok hid shows it again with its shared state; only when that state
// is cleared does the reopen go on to change it for everyone.
func (s *Server) clearLocal(ctx context.Context, tx *sql.Tx, tok *Token, n *Notification, action, via string) (*Notification, []Event, error) {
	switch {
	case action == "dismiss" && !n.Final():
		local, err := localClears(ctx, tx)
		if err != nil || !local[tok.ID] {
			return nil, nil, err
		}
		ev, err := hide(ctx, tx, tok, n, via)
		if err != nil || ev == nil {
			return hiddenView(tok, n), nil, err
		}
		return ev.Notification, []Event{*ev}, nil
	case action == "reopen":
		res, err := tx.ExecContext(ctx, `DELETE FROM hidden WHERE token_id = ? AND notification_id = ?`, tok.ID, n.ID)
		if err != nil {
			return nil, nil, err
		}
		if k, _ := res.RowsAffected(); k == 0 || n.Final() {
			return nil, nil, nil
		}
		c := *n
		return n, []Event{{Type: EventState, Notification: &c, Via: via, Only: tok.ID}}, nil
	}
	return nil, nil, nil
}

// hiddenSet is what a stream's device hid, kept current by its own events.
type hiddenSet map[string]bool

func (s *Server) hiddenOf(ctx context.Context, tok *Token) (hiddenSet, error) {
	h := hiddenSet{}
	rows, err := s.db.QueryContext(ctx, `SELECT notification_id FROM hidden WHERE token_id = ?`, tok.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		h[id] = true
	}
	return h, rows.Err()
}

// passes reports whether tok's stream gets e. An event for one device goes
// to it alone; a card tok hid gets nothing more until tok reopens it, but a
// clear from elsewhere (it shows cleared already, so it is harmless).
func (h hiddenSet) passes(tok *Token, e Event) bool {
	if e.Only != "" && e.Only != tok.ID {
		return false
	}
	if e.Only == "" && len(h) == 0 {
		return true
	}
	n := Notification{ID: e.Notification.ID, State: e.Notification.State}
	if n.ID == "" { // from the log: only the topic was read
		var c struct{ Notification struct{ ID, State string } }
		json.Unmarshal(e.Raw, &c)
		n.ID, n.State = c.Notification.ID, c.Notification.State
	}
	if e.Only != "" {
		if n.State == Dismissed {
			h[n.ID] = true
		} else {
			delete(h, n.ID)
		}
		return true
	}
	if n.Final() { // a real clear ends every hide of it (write drops the rows)
		delete(h, n.ID)
		return true
	}
	return !h[n.ID]
}

func (s *Server) clearsRoutes() {
	s.api("PUT /api/v1/devices/{id}/clears", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var p struct {
			Everywhere *bool `json:"everywhere"`
		}
		if err := decodeJSON(r, &p); err != nil {
			return err
		}
		if p.Everywhere == nil {
			return errf(400, "bad_request", "everywhere is required")
		}
		d, err := s.SetClears(r.Context(), tok, r.PathValue("id"), *p.Everywhere)
		if err != nil {
			return err
		}
		writeJSON(w, 200, d)
		return nil
	})
}
