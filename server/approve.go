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

// notOwn refuses tok when it is in the token tree whose top is sender.
func notOwn(ctx context.Context, q querier, tok *Token, sender string) error {
	mine, err := topIn(ctx, q, tok.ID)
	if err == nil && mine == sender {
		err = errf(403, "own_request", "a token cannot answer a request it sent")
	}
	return err
}

// confirmLink is what a confirm prompt holds, server side only: which
// answer an ask device gave, to which question as it stood then.
type confirmLink struct {
	Target, Reply, Asker, AskerName string
	Seq                             int64
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
	link, _ := json.Marshal(confirmLink{n.ID, reply, tok.ID, tok.Name, n.Seq})
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
// reopening p cannot revive the tap.
func (s *Server) confirmed(ctx context.Context, tx *sql.Tx, tok *Token, p *Notification, reply, via string) ([]Event, error) {
	var l confirmLink
	if err := json.Unmarshal([]byte(p.confirm), &l); err != nil {
		return nil, err
	}
	n, err := getNotification(ctx, tx, `id = ?`, l.Target)
	if isNotFound(err) {
		err = errf(409, "conflict", "the question is gone")
	}
	if err == nil {
		err = notOwn(ctx, tx, tok, n.sender)
	}
	if err != nil || reply != "confirm" {
		p.confirm = ""
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

// SetApprove changes device id's setting. tok must itself be allowed, not
// limited to some topics, and see the device (as in Devices). It may lower
// its own setting but never raise its own or any token above it, so a
// denied token cannot allow itself through a browser or phone it made.
// ponytail: an allowed full admin can still mint and raise a separate token;
// that is the user's own authority, and the Ask flow will add a confirm.
func (s *Server) SetApprove(ctx context.Context, tok *Token, id, approve string) (LinkedDevice, error) {
	var d LinkedDevice
	if err := tok.need(ScopeRead, ""); err != nil {
		return d, err
	}
	if tok.limited() {
		return d, errf(403, "forbidden", "a token limited to some topics cannot change who may approve")
	}
	if err := checkApprove(approve); err != nil {
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
		err = errf(403, "approve_denied", "only an allowed device can change who may approve")
	}
	if err == nil && approveRank[approve] > approveRank[d.Approve] {
		var up int // id is tok or a token tok hangs under
		err = tx.QueryRowContext(ctx, `WITH RECURSIVE up(id) AS
		  (SELECT ? UNION SELECT parent_id FROM sessions JOIN up ON token_id = up.id)
		  SELECT COUNT(*) FROM up WHERE id = ?`, tok.ID, id).Scan(&up)
		if err == nil && up > 0 {
			err = errf(403, "forbidden", "a device cannot raise its own setting or the one it hangs under")
		}
	}
	if err == nil {
		d.Approve = approve
		if _, err = tx.ExecContext(ctx, `UPDATE tokens SET approve = ? WHERE id = ?`, approve, id); err == nil {
			err = tx.Commit()
		}
	}
	s.mu.Unlock()
	if err == nil {
		s.devicesChanged.fire()
	}
	return d, err
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
}
