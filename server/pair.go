package server

import (
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rsc.io/qr"
)

// Pairing (issue #74): a browser signs in by having a device that is already
// signed in approve it. The QR code holds only a short-lived code. The browser
// that asked also holds a secret in a cookie, so a copied or photographed code
// cannot be claimed from any other browser, and the code alone approves nothing.

const (
	pairCookie = "clrd_pair"
	pairTTL    = 2 * time.Minute
	// ponytail: one global cap, no per-IP limit (the client IP is not trusted
	// behind a proxy). A flood only delays sign-ins by pairTTL; add a per-IP
	// limit with #136.
	maxPairs = 64
)

type pairing struct {
	secret   string
	name     string // the browser, from its User-Agent
	created  time.Time
	approver *Token // set on approve; the browser gets this token's access
	denied   bool
	// owner is set on a phone link (link.go): the browser token that made it.
	// Pair and link codes share this map and its cap, never each other's routes.
	owner    *Token
	approved bool
}

// pairingLocked returns the live pair (link false) or link (link true)
// request for code, dropping expired ones. Call with pairMu held.
func (s *Server) pairingLocked(code string, link bool) *pairing {
	for c, p := range s.pairs {
		if time.Since(p.created) > pairTTL {
			delete(s.pairs, c)
		}
	}
	if p := s.pairs[code]; p != nil && (p.owner != nil) == link {
		return p
	}
	return nil
}

// addPairing stores p under a new code, or returns 429 when too many wait.
func (s *Server) addPairing(p *pairing) (string, error) {
	code := randomString(22) // 22 base62 chars is 130 bits
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	s.pairingLocked("", false)
	if s.pairs == nil {
		s.pairs = map[string]*pairing{}
	}
	n := 0 // pair and link codes are capped apart, so public sign-in codes cannot crowd out a signed-in user's link
	for _, q := range s.pairs {
		if (q.owner != nil) == (p.owner != nil) {
			n++
		}
	}
	if n >= maxPairs {
		return "", errf(429, "busy", "too many codes waiting; try again in a minute")
	}
	p.created = time.Now()
	s.pairs[code] = p
	return code, nil
}

// writeCode answers a create: the code, its link at path and that link as a QR code.
func (s *Server) writeCode(w http.ResponseWriter, r *http.Request, path, code string) error {
	link := s.baseFor(r) + path + "?c=" + code
	img, err := qr.Encode(link, qr.M)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{
		"code":       code,
		"url":        link,
		"qr":         "data:image/png;base64," + base64.StdEncoding.EncodeToString(img.PNG()),
		"expires_at": time.Now().Add(pairTTL).UTC(),
	})
	return nil
}

func (s *Server) pairRoutes() {
	s.route("POST /api/v1/pair", s.handlePairCreate)
	s.route("POST /api/v1/pair/claim", s.handlePairClaim)
	s.route("GET /pair", s.handlePairPage)
	s.api("GET /api/v1/pair/{code}", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		s.pairMu.Lock()
		p := s.pairingLocked(r.PathValue("code"), false)
		var name string
		var created time.Time
		ok := p != nil && p.approver == nil && !p.denied
		if ok {
			name, created = p.name, p.created
		}
		s.pairMu.Unlock()
		if !ok {
			return errPairGone
		}
		writeJSON(w, 200, map[string]any{"name": name, "created_at": created.UTC()})
		return nil
	})
	s.api("POST /api/v1/pair/{code}/approve", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		return s.decidePair(w, r, tok, true)
	})
	s.api("POST /api/v1/pair/{code}/deny", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		return s.decidePair(w, r, tok, false)
	})
}

var errPairGone = errf(404, "not_found", "that code expired or was already used")

// handlePairCreate starts a sign-in from this browser: a code for the QR and
// a secret cookie that only this browser holds.
func (s *Server) handlePairCreate(w http.ResponseWriter, r *http.Request) error {
	if !s.sameOrigin(r) {
		return errf(403, "forbidden", "sign-in must come from this site")
	}
	secret := randomString(32)
	code, err := s.addPairing(&pairing{secret: secret, name: deviceName("", r.UserAgent())})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: pairCookie, Value: secret, Path: s.Path("/api/v1/pair"), MaxAge: int(pairTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.https(r) || r.Header.Get("X-Forwarded-Proto") == "https"})
	return s.writeCode(w, r, "/pair", code)
}

// handlePairClaim is polled by the waiting browser. Once approved it signs
// the browser in, with the approver as parent: same access, revoked with it.
func (s *Server) handlePairClaim(w http.ResponseWriter, r *http.Request) error {
	if !s.sameOrigin(r) {
		return errf(403, "forbidden", "sign-in must come from this site")
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	c, err := r.Cookie(pairCookie)
	if err != nil {
		return errf(410, "gone", "no sign-in waiting in this browser")
	}
	s.pairMu.Lock()
	p := s.pairingLocked(in.Code, false)
	if p == nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(p.secret)) != 1 {
		s.pairMu.Unlock()
		return errf(410, "gone", "that code expired or was already used")
	}
	if p.approver == nil && !p.denied {
		s.pairMu.Unlock()
		writeJSON(w, 202, map[string]string{"state": "waiting"})
		return nil
	}
	delete(s.pairs, in.Code) // single use
	s.pairMu.Unlock()
	if p.denied {
		return errf(410, "gone", "the sign-in was denied")
	}
	if _, err := s.startSession(w, r, p.approver, p.name); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: pairCookie, Path: s.Path("/api/v1/pair"), MaxAge: -1})
	writeJSON(w, 200, map[string]string{"state": "approved"})
	return nil
}

func (s *Server) decidePair(w http.ResponseWriter, r *http.Request, tok *Token, approve bool) error {
	code := r.PathValue("code")
	s.pairMu.Lock()
	p := s.pairingLocked(code, false)
	ok := p != nil && p.approver == nil && !p.denied
	name := ""
	if ok {
		name = p.name
		if approve {
			p.approver = tok
		} else {
			p.denied = true
		}
	}
	s.pairMu.Unlock()
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") { // from the /pair page
		msg := "Denied. That browser was not signed in."
		if approve {
			msg = "Approved. " + name + " signs itself in now. You can close this page."
		}
		if !ok {
			msg = "That code expired or was already used. Make a new one on the computer."
		}
		return s.renderPairPage(w, map[string]any{"Msg": msg})
	}
	if !ok {
		return errPairGone
	}
	if !approve {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	writeJSON(w, 200, map[string]string{"name": name})
	return nil
}

// handlePairPage is where the QR code leads, opened by a phone camera. Signed
// in here: Approve or Deny. Not signed in: hand the code to the clrd app.
func (s *Server) handlePairPage(w http.ResponseWriter, r *http.Request) error {
	code := r.URL.Query().Get("c")
	s.pairMu.Lock()
	p := s.pairingLocked(code, false)
	name := ""
	if p != nil && p.approver == nil && !p.denied {
		name = p.name
	}
	s.pairMu.Unlock()
	if name == "" {
		return s.renderPairPage(w, map[string]any{"Msg": "That code expired or was already used. Make a new one on the computer."})
	}
	data := map[string]any{"Code": code, "Name": name}
	if tok := s.auth(r, true); tok != nil {
		data["As"] = tok.Name
	} else {
		data["AppLink"] = template.URL("clrd://pair?" + url.Values{"server": {s.baseFor(r)}, "code": {code}}.Encode())
	}
	return s.renderPairPage(w, data)
}

func (s *Server) renderPairPage(w http.ResponseWriter, data map[string]any) error {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "same-origin") // the code is in the URL; no-referrer would send Origin: null and break Approve
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	if data["Title"] == nil {
		data["Title"] = "Sign in a browser"
	}
	data["Base"] = s.prefix
	return pairPage.Execute(w, data)
}

var pairPage = template.Must(template.New("pair").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><title>{{.Title}}: clrd</title>
<style>body{font:16px system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem;color:#0F1114;background:#F5F3EE}
@media (prefers-color-scheme:dark){body{color:#F5F3EE;background:#0F1114}}
button,a.btn{display:inline-block;font:inherit;padding:.6rem 1rem;border:1px solid #DEDAD1;border-radius:12px;background:transparent;color:inherit;text-decoration:none;margin:0 .5rem .5rem 0}
.go{background:#0B7A55;color:#fff;border:0}</style></head><body>
<h1>{{.Title}}</h1>
{{if .Msg}}<p>{{.Msg}}</p>{{else if .Link}}
<p><a class="btn go" href="{{.AppLink}}">Open in the clrd app</a></p>
<p>No app yet? Install clrd on this phone, then tap Scan QR code inside the app and scan the code again.</p>
{{else}}
<p><b>{{.Name}}</b> wants to sign in to your clrd inbox.</p>
<p>Only approve if you just scanned this code on your own screen. Never approve a code someone sent you.</p>
{{if .As}}<p>It will get the same access as <b>{{.As}}</b>.</p>
<form method="post" action="{{.Base}}/api/v1/pair/{{.Code}}/approve" style="display:inline"><button class="go">Approve</button></form>
<form method="post" action="{{.Base}}/api/v1/pair/{{.Code}}/deny" style="display:inline"><button>Deny</button></form>
{{else}}<p><a class="btn go" href="{{.AppLink}}">Open in the clrd app</a></p>
<p>The app asks you to approve. Needs the clrd app, signed in to this server.</p>{{end}}
{{end}}
</body></html>`))
