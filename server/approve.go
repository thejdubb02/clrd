package server

import (
	"context"
	"net/http"
)

// Who may answer approval requests (#187). Every token and device has a
// setting: allowed ones may tap a reply action, denied ones may not. Phones
// and browsers the user links inherit it from the token they hang under;
// the setup token is allowed and every other new token denied.
const (
	ApproveAllowed = "allowed"
	ApproveDenied  = "denied"
)

// approveOf reads id's setting through q, never a copy that may be stale.
func approveOf(ctx context.Context, q querier, id string) (string, error) {
	var v string
	err := q.QueryRowContext(ctx, `SELECT approve FROM tokens WHERE id = ?`, id).Scan(&v)
	return v, err
}

// mayApprove refuses a reply action from tok when its setting is denied, or
// when the notification came from the same token tree: a token never
// answers its own request, through a browser or phone made from it either.
func mayApprove(ctx context.Context, q querier, tok *Token, sender string) error {
	v, err := approveOf(ctx, q, tok.ID)
	if err != nil {
		return err
	}
	if v != ApproveAllowed {
		return errf(403, "approve_denied", "this device may not answer approval requests; an allowed device can change that in Settings")
	}
	if sender == "" {
		return nil
	}
	mine, err := topIn(ctx, q, tok.ID) // sender is already its tree's top
	if err == nil && mine == sender {
		err = errf(403, "own_request", "a token cannot answer a request it sent")
	}
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
	if err := tok.need(ScopeRead, ""); err != nil {
		return d, err
	}
	if tok.limited() {
		return d, errf(403, "forbidden", "a token limited to some topics cannot change who may approve")
	}
	if approve != ApproveAllowed && approve != ApproveDenied {
		return d, errf(400, "bad_request", "approve must be %s or %s", ApproveAllowed, ApproveDenied)
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
	if err == nil && approve == ApproveAllowed && d.Approve != ApproveAllowed {
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
