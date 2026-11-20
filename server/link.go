package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"
)

// Linking a phone (issue #74): pairing run the other way. A signed-in browser
// shows a QR code, the clrd app scans it and claims the code with a secret it
// made, and the browser approves that phone by name. The code alone gets
// nothing. Link codes live in s.pairs next to pair codes (pair.go), with an
// owner set, so they share its expiry and single use; each kind has its own cap.

func (s *Server) linkRoutes() {
	s.api("POST /api/v1/link", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		code, err := s.addPairing(&pairing{owner: tok})
		if err != nil {
			return err
		}
		return s.writeCode(w, "/link", code)
	})
	s.route("POST /api/v1/link/claim", s.handleLinkClaim)
	s.route("GET /link", s.handleLinkPage)
	s.api("GET /api/v1/link/{code}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		s.pairMu.Lock()
		p := s.ownLinkLocked(r.PathValue("code"), tok)
		var name, state, check string
		if p != nil {
			name, state = p.name, "waiting"
			if p.secret != "" {
				state, check = "claimed", linkCheck(p.secret)
			}
		}
		s.pairMu.Unlock()
		if p == nil {
			return errPairGone
		}
		writeJSON(w, 200, map[string]string{"name": name, "state": state, "check": check})
		return nil
	})
	s.api("POST /api/v1/link/{code}/approve", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		s.pairMu.Lock()
		p := s.ownLinkLocked(r.PathValue("code"), tok)
		claimed := p != nil && p.secret != ""
		if claimed {
			// A fresh window for the bound phone to collect its token, so an
			// approval near the end of the code's life is not lost (#184).
			p.approved, p.created = true, time.Now()
		}
		s.pairMu.Unlock()
		if p == nil {
			return errPairGone
		}
		if !claimed {
			return errf(409, "conflict", "no phone has scanned this code yet")
		}
		writeJSON(w, 200, map[string]string{"name": p.name})
		return nil
	})
	s.api("POST /api/v1/link/{code}/deny", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		code := r.PathValue("code")
		s.pairMu.Lock()
		p := s.ownLinkLocked(code, tok)
		if p != nil {
			delete(s.pairs, code) // the phone's next claim gets 410
		}
		s.pairMu.Unlock()
		if p == nil {
			return errPairGone
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// ownLinkLocked is the live link for code if tok made it. Call with pairMu held.
func (s *Server) ownLinkLocked(code string, tok *Token) *pairing {
	if p := s.pairingLocked(code, true); p != nil && p.owner.ID == tok.ID {
		return p
	}
	return nil
}

// handleLinkClaim is polled by the phone. The first claim binds its secret
// and name to the code; once the browser approves, it gets its own token.
func (s *Server) handleLinkClaim(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Code   string `json:"code"`
		Secret string `json:"secret"`
		Name   string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if len(in.Secret) < 32 {
		return errf(400, "bad_request", "secret must be at least 32 characters")
	}
	s.pairMu.Lock()
	p := s.pairingLocked(in.Code, true)
	if p != nil && p.secret == "" {
		p.secret, p.name = in.Secret, deviceName(in.Name, "")
	}
	if p == nil || subtle.ConstantTimeCompare([]byte(in.Secret), []byte(p.secret)) != 1 {
		s.pairMu.Unlock()
		return errf(410, "gone", "that code expired or was already used")
	}
	if !p.approved {
		s.pairMu.Unlock()
		writeJSON(w, 202, map[string]string{"state": "waiting"})
		return nil
	}
	delete(s.pairs, in.Code) // single use
	s.pairMu.Unlock()
	ctx := context.WithoutCancel(r.Context())
	parent, err := s.rootOf(ctx, p.owner.ID)
	if err != nil {
		return err
	}
	_, raw, err := s.childToken(ctx, p.owner, parent, p.name, true)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]string{"state": "approved", "token": raw, "name": p.name})
	return nil
}

// rootOf walks up from id past browser sessions to the first token that is
// not one: a typed-in token or a linked phone. A phone linked there outlives
// the sign-out of every browser on the way (even one paired by another
// browser, issue #185) but not a revoke of that token.
func (s *Server) rootOf(ctx context.Context, id string) (string, error) {
	for {
		var parent string
		var phone bool
		err := s.db.QueryRowContext(ctx, `SELECT parent_id, phone FROM sessions WHERE token_id = ?`, id).Scan(&parent, &phone)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && phone) {
			return id, nil
		}
		if err != nil {
			return "", err
		}
		id = parent
	}
}

// handleLinkPage is where the QR code leads when a phone camera, not the
// app, scans it: hand the code to the app.
func (s *Server) handleLinkPage(w http.ResponseWriter, r *http.Request) error {
	code := r.URL.Query().Get("c")
	s.pairMu.Lock()
	p := s.pairingLocked(code, true)
	live := p != nil && p.secret == ""
	s.pairMu.Unlock()
	data := map[string]any{"Title": "Link a phone"}
	if !live {
		data["Msg"] = "That code expired or was already used. Make a new one in Settings on the computer."
	} else {
		data["Link"] = true
		data["AppLink"] = template.URL("clrd://link?" + url.Values{"server": {s.opt.BaseURL}, "code": {code}}.Encode())
	}
	return s.renderPairPage(w, data)
}

// linkCheck is the 4-digit number the phone shows and the browser asks the
// person to match, derived from the phone's secret. Someone who photographed
// the QR and claimed it first cannot make the real phone show their number.
func linkCheck(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return fmt.Sprintf("%04d", binary.BigEndian.Uint32(h[:4])%10000)
}
