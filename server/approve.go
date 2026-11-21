package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
)

// Who may answer approval requests (#187). Every token and device has a
// setting: allowed ones may tap a reply action, ask ones may tap but it
// counts only once an allowed device confirms it, denied ones may not. Phones
// and browsers the user links inherit it from the token they hang under;
// the setup token is allowed and every other new token denied.
const (
	ApproveAllowed = "allowed"
	ApproveAsk     = "ask"
	ApproveDenied  = "denied"
)

// approveRank orders the settings: a change to a higher one is a raise.
var approveRank = map[string]int{ApproveDenied: 0, ApproveAsk: 1, ApproveAllowed: 2}

func checkApprove(v string) error {
	if _, ok := approveRank[v]; !ok {
		return errf(400, "bad_request", "approve must be %s, %s or %s", ApproveAllowed, ApproveAsk, ApproveDenied)
	}
	return nil
}

// approveOf reads id's setting through q, never a copy that may be stale.
func approveOf(ctx context.Context, q querier, id string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT approve FROM tokens WHERE id = ?`, id).Scan(&v)
	return v, err
}

// mayApprove returns tok's setting, or refuses a reply action from tok when
// it is denied, or when the notification came from the same token tree: a
// token never answers its own request, through a browser or phone made from
// it either. Only an allowed device may answer a confirm prompt.
func mayApprove(ctx context.Context, q querier, tok *Token, n *Notification) (string, error) {
	v, err := approveOf(ctx, q, tok.ID)
	if err == nil && (v == ApproveDenied || (v == ApproveAsk && n.confirm != "")) {
		err = errf(403, "approve_denied", "this device may not answer approval requests; an allowed device can change that in Settings")
	}
	if err == nil && n.sender != "" {
		err = notOwn(ctx, q, tok, n.sender)
	}
	return v, err
}

// notOwn refuses tok when it is in the token tree whose top is sender. It
// stays the tree, not the user (#143): a person answers their own agent.
func notOwn(ctx context.Context, q querier, tok *Token, sender string) error {
	mine, err := topIn(ctx, q, tok.ID)
	if err == nil && mine == sender {
		err = errf(403, "own_request", "a token cannot answer a request it sent")
	}
	return err
}

// confirmLink is what a confirm prompt holds, server side only: which
// answer an ask device gave, to which question as it stood then. With Kind
// "approve" it is instead device Asker's request to raise its own setting
// From one To Reply, and Target is empty.
type confirmLink struct {
	Target, Reply, Asker, AskerName string
	Seq                             int64
	Kind, From                      string `json:",omitempty"`
}

// AskApprove posts tok's request to raise its own setting to approve, as a
// confirm prompt on topic ("agents" if empty). It counts once an allowed
// device that could make the change itself (as in SetApprove) confirms it.
// A repeat request returns the prompt already open, with no new event.
func (s *Server) AskApprove(ctx context.Context, tok *Token, topic, approve string) (*Notification, error) {
	if tok == nil {
		return nil, errf(401, "unauthorized", noToken)
	}
	if topic == "" {
		topic = "agents"
	}
	if err := checkApprove(approve); err != nil {
		return nil, err
	}
	if err := validTopic(topic); err != nil {
		return nil, err
	}
	if err := tok.need(ScopePublish, topic); err != nil {
		return nil, err
	}
	var out *Notification
	_, err := s.write(ctx, func(tx *sql.Tx) ([]Event, error) {
		cur, err := approveOf(ctx, tx, tok.ID)
		if err != nil {
			return nil, err
		}
		if approveRank[approve] <= approveRank[cur] {
			return nil, errf(400, "bad_request", "this device is already %s; a request can only raise it", cur)
		}
		out, err = getNotification(ctx, tx, `topic = ? AND state NOT IN ('dismissed', 'resolved') AND json_extract(NULLIF(confirm, ''), '$.Kind') = 'approve'
		  AND json_extract(confirm, '$.Asker') = ? AND json_extract(confirm, '$.Reply') = ? AND json_extract(confirm, '$.From') = ?`, topic, tok.ID, approve, cur)
		if !isNotFound(err) {
			return nil, err
		}
		if err := s.publishLimit(tok, topic); err != nil {
			return nil, err
		}
		link, _ := json.Marshal(confirmLink{Reply: approve, Asker: tok.ID, AskerName: tok.Name, Kind: "approve", From: cur})
		t := now()
		out = &Notification{ID: s.newID(t), Topic: topic, Title: fmt.Sprintf("Confirm %s may approve: %s", tok.Name, approve),
			Message: fmt.Sprintf("%s asks to change who may approve from %s to %s for itself.", tok.Name, cur, approve),
			Format:  FormatText, Priority: 3, Tags: []string{},
			Actions: []Action{{Label: "Confirm", Reply: "confirm"}, {Label: "Reject", Reply: "reject"}},
			State:   Active, StateBy: tok.Name, StateByID: tok.ID, CreatedAt: t, confirm: string(link)}
		return []Event{{Type: EventCreated, Notification: out}}, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// askConfirm is an ask device's tap on n: n stays unanswered, and a prompt
// on the same topic asks an allowed device to confirm it. A repeat tap
// returns the prompt already open, with no new event.
func (s *Server) askConfirm(ctx context.Context, tx *sql.Tx, tok *Token, n *Notification, reply string) (*Notification, []Event, error) {
	p, err := getNotification(ctx, tx, `state NOT IN ('dismissed', 'resolved') AND json_extract(NULLIF(confirm, ''), '$.Target') = ?
	  AND json_extract(confirm, '$.Asker') = ? AND json_extract(confirm, '$.Reply') = ? AND json_extract(confirm, '$.Seq') = ?`, n.ID, tok.ID, reply, n.Seq)
	if !isNotFound(err) {
		return p, nil, err
	}
	// No sender: the asker is ask so it cannot confirm, while the phone it
	// shares a top token with must be able to.
	link, _ := json.Marshal(confirmLink{Target: n.ID, Reply: reply, Asker: tok.ID, AskerName: tok.Name, Seq: n.Seq})
	label := n.Actions[slices.IndexFunc(n.Actions, func(a Action) bool { return a.Reply == reply })].Label
	t := now()
	p = &Notification{ID: s.newID(t), Topic: n.Topic, Title: fmt.Sprintf("Confirm %s's answer: %s", tok.Name, label),
		Message: n.Title + "\n\n" + n.Message, Format: n.Format, Priority: n.Priority, Tags: []string{}, Icon: n.Icon,
		Actions: []Action{{Label: "Confirm", Reply: "confirm"}, {Label: "Reject", Reply: "reject"}},
		State:   Active, StateBy: tok.Name, StateByID: tok.ID, CreatedAt: t, confirm: string(link)}
	return p, []Event{{Type: EventCreated, Notification: p}}, nil
}

// confirmed answers prompt p for tok (allowed, outside the question's tree).
// On confirm it applies the ask device's tap to the question, which must be
// unanswered and unchanged since that tap. On reject p drops its link, so
// reopening p cannot revive the tap. A confirmed setting request sets raised.
func (s *Server) confirmed(ctx context.Context, tx *sql.Tx, tok *Token, p *Notification, reply, via string, raised *bool) ([]Event, error) {
	var l confirmLink
	if err := json.Unmarshal([]byte(p.confirm), &l); err != nil {
		return nil, err
	}
	if reply != "confirm" || l.Kind == "approve" { // a setting request is spent once answered
		p.confirm = ""
	}
	if l.Kind == "approve" {
		return nil, s.raiseConfirmed(ctx, tx, tok, l, reply, raised)
	}
	n, err := getNotification(ctx, tx, `id = ?`, l.Target)
	if isNotFound(err) {
		err = errf(409, "conflict", "the question is gone")
	}
	if err == nil {
		err = notOwn(ctx, tx, tok, n.sender)
	}
	if err != nil || reply != "confirm" {
		return nil, err
	}
	if err := s.changedSince(ctx, tx, n, l.Seq); err != nil {
		return nil, err
	}
	if n.Reply != "" || n.Final() {
		return nil, errf(409, "conflict", "already answered or cleared")
	}
	n.Reply, n.State, n.SnoozeUntil, n.StateBy, n.StateByID = l.Reply, Acknowledged, nil, l.AskerName, l.Asker
	n.stopRepeat()
	return []Event{{Type: EventState, Notification: n, Via: via, replied: true}}, nil
}

// raiseConfirmed applies device l.Asker's setting request for tok, under the
// same rules as SetApprove, if the device is still at the setting it asked
// from. A reject changes nothing.
func (s *Server) raiseConfirmed(ctx context.Context, tx *sql.Tx, tok *Token, l confirmLink, reply string, raised *bool) error {
	if reply != "confirm" {
		return nil
	}
	if err := approveGate(tok, l.Reply); err != nil {
		return err
	}
	d, err := s.device(ctx, tx, tok, l.Asker)
	if isNotFound(err) {
		err = errf(409, "conflict", "the device is gone or not one this device can see")
	}
	if err == nil && d.Approve != l.From {
		err = errf(409, "conflict", "the device's setting changed since it asked")
	}
	if err == nil {
		err = setApprove(ctx, tx, tok, d, l.Reply)
	}
	*raised = err == nil
	return err
}

// SetApprove changes device id's setting. tok must itself be allowed, not
// limited to some topics, and see the device (as in Devices). It may lower
// its own setting but never raise its own or any token above it, so a
// denied token cannot allow itself through a browser or phone it made.
// ponytail: an allowed full admin can still mint and raise a separate token;
// that is the user's own authority, and the Ask flow will add a confirm.
func (s *Server) SetApprove(ctx context.Context, tok *Token, id, approve string) (LinkedDevice, error) {
	var d LinkedDevice
	if err := approveGate(tok, approve); err != nil {
		return d, err
	}
	tx, d, err := s.deviceTx(ctx, tok, id)
	if tx != nil {
		defer tx.Rollback()
	}
	if err == nil {
		err = setApprove(ctx, tx, tok, d, approve)
	}
	if err == nil {
		d.Approve = approve
		err = tx.Commit()
	}
	s.mu.Unlock()
	if err == nil {
		s.devicesChanged.fire()
	}
	return d, err
}

// approveGate is what SetApprove checks before it looks at any device.
func approveGate(tok *Token, approve string) error {
	if err := tok.need(ScopeRead, ""); err != nil {
		return err
	}
	if tok.limited() {
		return errf(403, "forbidden", "a token limited to some topics cannot change who may approve")
	}
	return checkApprove(approve)
}

// setApprove sets device d (from tok's device list) to approve inside tx.
func setApprove(ctx context.Context, tx *sql.Tx, tok *Token, d LinkedDevice, approve string) error {
	mine, err := approveOf(ctx, tx, tok.ID)
	if err == nil && mine != ApproveAllowed {
		err = errf(403, "approve_denied", "only an allowed device can change who may approve")
	}
	if err == nil && approveRank[approve] > approveRank[d.Approve] {
		var up int // d is tok or a token tok hangs under
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE up(id) AS
		  (SELECT ? UNION SELECT parent_id FROM sessions JOIN up ON token_id = up.id)
		  SELECT COUNT(*) FROM up WHERE id = ?`, tok.ID, d.ID).Scan(&up)
		if err == nil && up > 0 {
			err = errf(403, "forbidden", "a device cannot raise its own setting or the one it hangs under")
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE tokens SET approve = ? WHERE id = ?`, approve, d.ID)
	}
	return err
}

func (s *Server) approveRoutes() {
	s.api("PUT /api/v1/devices/{id}/approve", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var p struct {
			Approve string `json:"approve"`
		}
		if err := decodeJSON(r, &p); err != nil {
			return err
		}
		d, err := s.SetApprove(r.Context(), tok, r.PathValue("id"), p.Approve)
		if err != nil {
			return err
		}
		writeJSON(w, 200, d)
		return nil
	})
	s.api("POST /api/v1/devices/approve-request", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		var p struct {
			Approve, Topic string
		}
		if err := decodeJSON(r, &p); err != nil {
			return err
		}
		n, err := s.AskApprove(r.Context(), tok, p.Topic, p.Approve)
		if err != nil {
			return err
		}
		writeJSON(w, 202, n)
		return nil
	})
}
