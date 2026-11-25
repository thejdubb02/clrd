package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Web Push (#164): a browser that subscribes gets an encrypted push (RFC
// 8291) signed with the server's VAPID key (RFC 8292) wherever its web inbox
// would ring: a created, repeat or snooze wake event on an active, unsilenced
// notification it may read and is not left out of by "devices". Pushes are
// best effort: no retry, no queue on disk.

var b64 = base64.RawURLEncoding

// pushMax is the most plaintext one push carries: a 4096-byte record less
// the 86-byte header, the 0x02 delimiter and the 16-byte tag.
const pushMax = 4096 - 86 - 1 - 16

type pushSub struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// pushMsg is what a push carries (docs/SPEC.md section 9).
type pushMsg struct {
	Type     string   `json:"type"`
	Via      string   `json:"via,omitempty"`
	Seq      int64    `json:"seq"`
	ID       string   `json:"id"`
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
	Icon     string   `json:"icon,omitempty"`
}

// loadVAPID makes the server's VAPID key on first start and reads it back.
func loadVAPID(db *sql.DB) (*ecdsa.PrivateKey, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO settings (key, value) VALUES ('vapid', ?)`, b64.EncodeToString(der)); err != nil {
		return nil, err
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'vapid'`).Scan(&v); err != nil {
		return nil, err
	}
	if der, err = b64.DecodeString(v); err != nil {
		return nil, err
	}
	p, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	if k, ok := p.(*ecdsa.PrivateKey); ok && k.Curve == elliptic.P256() {
		return k, nil
	}
	return nil, errors.New("vapid key in settings is not a P-256 key")
}

func (s *Server) pushRoutes() {
	s.api("GET /api/v1/push", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		var n int
		if err := s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM push_subs WHERE token_id = ?`, tok.ID).Scan(&n); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"public_key": s.vapidPub, "subscribed": n > 0})
		return nil
	})
	s.api("PUT /api/v1/push", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		var p pushSub
		if err := decodeJSON(r, &p); err != nil {
			return err
		}
		if _, _, err := p.keys(); err != nil {
			return err
		}
		// One per device, and an endpoint on one device only: a browser
		// signed in again replaces the row its old session had.
		_, err := s.db.ExecContext(r.Context(), `INSERT OR REPLACE INTO push_subs (endpoint, token_id, p256dh, auth, created_at) VALUES (?, ?, ?, ?, ?)`,
			p.Endpoint, tok.ID, p.Keys.P256dh, p.Keys.Auth, ms(now()))
		if err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
	s.api("DELETE /api/v1/push", func(w http.ResponseWriter, r *http.Request, tok *Token) error {
		if err := tok.need(ScopeRead, ""); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(r.Context(), `DELETE FROM push_subs WHERE token_id = ?`, tok.ID); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})
}

// keys checks a subscription and returns its decoded keys.
func (p pushSub) keys() (ua, auth []byte, err error) {
	if u, err := url.Parse(p.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" || len(p.Endpoint) > 2048 {
		return nil, nil, errf(400, "bad_request", "endpoint must be an https URL")
	}
	ua, err = b64.DecodeString(strings.TrimRight(p.Keys.P256dh, "="))
	if err == nil {
		_, err = ecdh.P256().NewPublicKey(ua)
	}
	if err != nil {
		return nil, nil, errf(400, "bad_request", "keys.p256dh must be a base64url P-256 public key")
	}
	if auth, err = b64.DecodeString(strings.TrimRight(p.Keys.Auth, "=")); err != nil || len(auth) != 16 {
		return nil, nil, errf(400, "bad_request", "keys.auth must be 16 bytes, base64url")
	}
	return ua, auth, nil
}

// rings is when the web inbox rings for e (web/static/inbox.js), so a push
// goes out on the same events. Each device's own rules are in sendPushes.
func rings(e Event) bool {
	n := e.Notification
	return n != nil && n.State == Active && !n.Silenced && (e.Type == EventCreated || e.Type == EventRepeat || e.Via == "snooze_end")
}

// queuePush hands e to the push worker without waiting. Callers hold s.mu.
func (s *Server) queuePush(e Event) {
	if !rings(e) {
		return
	}
	select {
	case s.pushq <- e:
	default: // ponytail: a full queue drops the push; the inbox still has it
		s.log.Warn("web push queue full, push dropped", "seq", e.Seq)
	}
}

func (s *Server) pushLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-s.pushq:
			s.sendPushes(ctx, e)
		}
	}
}

type pushTarget struct {
	tokenID, endpoint, p256dh, auth string
}

// sendPushes pushes e to every subscribed device that may read its topic and
// that "devices" does not leave out (a left out device gets it silenced, and
// a silenced one never pushes).
func (s *Server) sendPushes(ctx context.Context, e Event) {
	rows, err := s.db.QueryContext(ctx, `SELECT token_id, endpoint, p256dh, auth FROM push_subs`)
	if err != nil {
		s.log.Error("web push", "err", err)
		return
	}
	var subs []pushTarget
	for rows.Next() {
		var p pushTarget
		if rows.Scan(&p.tokenID, &p.endpoint, &p.p256dh, &p.auth) == nil {
			subs = append(subs, p)
		}
	}
	rows.Close()
	n := e.Notification
	hid := map[string]bool{} // devices that cleared it for themselves (#767)
	if rows, err := s.db.QueryContext(ctx, `SELECT token_id FROM hidden WHERE notification_id = ?`, n.ID); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				hid[id] = true
			}
		}
		rows.Close()
	}
	body := pushPayload(e)
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, p := range subs {
		if len(n.Devices) > 0 && !slices.Contains(n.Devices, p.tokenID) || hid[p.tokenID] {
			continue
		}
		// Take the slot first so the check is fresh when the send starts.
		// Not tokenWhere: a push is not the device being seen.
		sem <- struct{}{}
		tok, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id = ?`, p.tokenID))
		if err != nil || s.withUser(ctx, tok) != nil || !tok.may(ScopeRead, n.Topic) {
			<-sem
			continue
		}
		if tok, err = s.withView(ctx, tok, false); err != nil || tok.view.hides(n.Topic) { // unsubscribed (#617)
			<-sem
			continue
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			code, err := s.push(ctx, p, body, n)
			switch {
			case err != nil:
				s.log.Warn("web push", "device", p.tokenID, "err", err)
				s.recordPushFailure(ctx, n.Topic)
			case code == 404 || code == 410: // the browser dropped it
				s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM push_subs WHERE endpoint = ? AND token_id = ? AND auth = ?`, p.endpoint, p.tokenID, p.auth)
			case code < 200 || code > 299:
				s.log.Warn("web push", "device", p.tokenID, "code", code)
				s.recordPushFailure(ctx, n.Topic)
			}
		}()
	}
	wg.Wait()
}

func (s *Server) recordPushFailure(ctx context.Context, topic string) {
	if _, err := s.db.ExecContext(context.WithoutCancel(ctx), `INSERT INTO push_failures (topic, at) VALUES (?, ?)`, topic, ms(now())); err != nil {
		s.log.Warn("web push failure record", "err", err)
	}
}

// pushPayload is e as a push carries it, cut to fit: the message first,
// then everything but type, seq, id and topic.
func pushPayload(e Event) []byte {
	n := e.Notification
	m := pushMsg{Type: e.Type, Via: e.Via, Seq: e.Seq, ID: n.ID, Topic: n.Topic, Title: n.Title, Message: n.Message,
		Priority: n.Priority, Tags: n.Tags, Click: n.Click, Icon: n.Icon}
	for {
		b, _ := json.Marshal(m)
		over := len(b) - pushMax
		if over <= 0 {
			return b
		}
		if m.Message == "" {
			b, _ = json.Marshal(pushMsg{Type: m.Type, Via: m.Via, Seq: m.Seq, ID: m.ID, Topic: m.Topic})
			return b
		}
		m.Message = strings.ToValidUTF8(m.Message[:max(len(m.Message)-over, 0)], "")
	}
}

// push sends one encrypted push and returns the push service's status.
func (s *Server) push(ctx context.Context, p pushTarget, body []byte, n *Notification) (int, error) {
	sub := pushSub{Endpoint: p.endpoint}
	sub.Keys.P256dh, sub.Keys.Auth = p.p256dh, p.auth
	ua, auth, err := sub.keys()
	if err != nil {
		return 0, err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return 0, err
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	enc, err := encryptPush(body, ua, auth, as, salt)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", p.endpoint, bytes.NewReader(enc))
	if err != nil {
		return 0, err
	}
	jwt, err := s.vapidJWT("https://" + strings.TrimSuffix(req.URL.Host, ":443"))
	if err != nil {
		return 0, err
	}
	urgency := "normal"
	if n.Priority >= 4 {
		urgency = "high"
	} else if n.Priority <= 2 {
		urgency = "low"
	}
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.vapidPub)
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", urgency)
	req.Header.Set("Topic", n.ID) // a ULID fits the 32 characters: a repeat replaces one not yet delivered
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := s.pushHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, nil
}

// encryptPush is RFC 8291 aes128gcm in one record, from the ephemeral key as
// and a 16-byte salt.
func encryptPush(plain, uaPub, authSecret []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, err
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	// hkdf.Key fails only on a length over 8160 bytes.
	ikm, _ := hkdf.Key(sha256.New, secret, authSecret, "WebPush: info\x00"+string(uaPub)+string(asPub), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	out := binary.BigEndian.AppendUint32(slices.Clone(salt), 4096)
	out = append(append(out, byte(len(asPub))), asPub...)
	return gcm.Seal(out, nonce, append(slices.Clip(plain), 2), nil), nil
}

// vapidJWT is the RFC 8292 token for one push service origin.
func (s *Server) vapidJWT(aud string) (string, error) {
	sub := "mailto:clrd@localhost"
	if s.baseSet && strings.HasPrefix(s.opt.BaseURL, "https://") {
		sub = strings.TrimSuffix(s.opt.BaseURL, "/")
	}
	claims, err := json.Marshal(map[string]any{"aud": aud, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": sub})
	if err != nil {
		return "", err
	}
	in := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(in))
	r, ss, err := ecdsa.Sign(rand.Reader, s.vapid, h[:])
	if err != nil {
		return "", err
	}
	return in + "." + b64.EncodeToString(append(r.FillBytes(make([]byte, 32)), ss.FillBytes(make([]byte, 32))...)), nil
}
