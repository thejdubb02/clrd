package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Issue #246: the full device list, unlink from another device and the log.
func TestUnlinkDevice(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	_, _, a := signIn(t, ts, `{"token":"`+typed+`","name":"browser a"}`, firefoxLinux, nil)
	_, _, b := signIn(t, ts, `{"token":"`+typed+`","name":"browser b"}`, firefoxLinux, nil)
	phone := linkPhone(t, ts, typed)
	pt := s.lookupToken(ctx, phone)
	_, paired, err := s.childToken(ctx, pt, pt.ID, "paired", false) // a browser the phone approved by QR
	if err != nil {
		t.Fatal(err)
	}
	other := mkToken(t, s, "other", []string{ScopeRead}, nil)

	devices := func(tok string) map[string]map[string]any {
		t.Helper()
		code, out := call(t, ts, "GET", "/api/v1/devices?all=1", tok, nil)
		if code != 200 {
			t.Fatalf("devices: %d", code)
		}
		raw, _ := json.Marshal(out)
		for _, secret := range []string{typed, a.Value, b.Value, phone, paired, other} {
			if strings.Contains(string(raw), secret) || strings.Contains(string(raw), hashToken(secret)) {
				t.Fatalf("device list leaks a secret or hash: %s", raw)
			}
		}
		byName := map[string]map[string]any{}
		for _, d := range out["devices"].([]any) {
			byName[d.(map[string]any)["name"].(string)] = d.(map[string]any)
		}
		return byName
	}
	mine := devices(a.Value)
	for name, kind := range map[string]string{"laptop": "token", "browser a": "browser", "browser b": "browser", "paired": "browser"} {
		if mine[name]["kind"] != kind {
			t.Errorf("%s: kind %v, want %s", name, mine[name]["kind"], kind)
		}
	}
	var phoneID string
	for name, d := range mine {
		if d["kind"] == "phone" {
			phoneID = d["id"].(string)
			if name == "" || d["via"] != "laptop" {
				t.Errorf("phone row: %v", d)
			}
		}
	}
	if phoneID == "" || mine["other"] != nil || mine["browser a"]["this"] != true || mine["browser b"]["this"] != false || mine["laptop"]["can_unlink"] != false {
		t.Fatalf("own list: %v", mine)
	}
	if all := devices(admin); all["other"] == nil {
		t.Fatal("a full admin should see every token")
	}
	// #263: the plain list (seen in the last 10 minutes) is scoped the same way.
	_, recent := call(t, ts, "GET", "/api/v1/devices", other, nil)
	if d := recent["devices"].([]any); len(d) != 1 || d[0].(map[string]any)["name"] != "other" {
		t.Fatalf("another family's reader saw these devices: %v", recent)
	}

	// A reader cannot unlink a device it did not approve, nor see it.
	if code, _ := call(t, ts, "DELETE", "/api/v1/devices/"+phoneID, other, nil); code != 404 {
		t.Fatalf("other token unlinking the phone: %d, want 404", code)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/notifications", phone, nil); code != 200 {
		t.Fatalf("phone after a refused unlink: %d", code)
	}
	if devices(other)["browser a"] != nil {
		t.Fatal("other token sees someone else's devices")
	}
	// A typed-in token is revoked with /api/v1/tokens, never unlinked here.
	if code, _ := call(t, ts, "DELETE", "/api/v1/devices/"+mine["laptop"]["id"].(string), a.Value, nil); code != 403 {
		t.Fatalf("unlinking the typed token: %d, want 403", code)
	}

	// Browser a unlinks browser b: b is signed out and its WebSocket closes now.
	ws := dialWS(t, ts.URL, b.Value, 0)
	ws.until(func(m msg) bool { return m.Type == "ready" })
	if code, _ := call(t, ts, "DELETE", "/api/v1/devices/"+mine["browser b"]["id"].(string), a.Value, nil); code != 204 {
		t.Fatalf("unlink b: %d", code)
	}
	if withCookie(t, ts, "GET", "/api/v1/topics", b) != 401 {
		t.Fatal("unlinked browser still has access")
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var ce websocket.CloseError
	if _, _, err := ws.c.Read(rctx); !errors.As(err, &ce) || ce.Reason != "token revoked" {
		t.Fatalf("unlinked browser's stream: %v", err)
	}

	// Unlinking the phone signs out the browser it approved too.
	if code, _ := call(t, ts, "DELETE", "/api/v1/devices/"+phoneID, a.Value, nil); code != 204 {
		t.Fatalf("unlink phone: %d", code)
	}
	if code, _ := call(t, ts, "GET", "/api/v1/topics", paired, nil); code != 401 {
		t.Fatalf("browser the phone approved: %d, want 401", code)
	}

	_, out := call(t, ts, "GET", "/api/v1/devices/log", a.Value, nil)
	var lines []string
	for _, e := range out["events"].([]any) {
		m := e.(map[string]any)
		lines = append(lines, m["action"].(string)+":"+m["device"].(string)+":"+m["by"].(string))
	}
	got := strings.Join(lines, " | ")
	for _, want := range []string{"unlinked:browser b:browser a", "signed in:browser a:laptop", "signed in:paired:" + pt.Name, "linked:" + pt.Name + ":laptop"} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q lacks %q", got, want)
		}
	}
	if !strings.HasPrefix(got, "unlinked:"+pt.Name) {
		t.Errorf("log not newest first: %q", got)
	}
	if _, o := call(t, ts, "GET", "/api/v1/devices/log", other, nil); len(o["events"].([]any)) != 0 {
		t.Errorf("other token sees someone else's log: %v", o)
	}
}

// The log keeps the newest maxDeviceLog lines per top token.
func TestDeviceLogBounded(t *testing.T) {
	s, _, _ := newTest(t)
	ctx := context.Background()
	for i := 0; i < maxDeviceLog+5; i++ {
		s.logDevice(ctx, "top", "signed in", "x", "id", "y")
	}
	s.logDevice(ctx, "else", "signed in", "x", "id", "y")
	var n int
	s.db.QueryRow(`SELECT count(*) FROM device_log WHERE owner = 'top'`).Scan(&n)
	if n != maxDeviceLog {
		t.Fatalf("log rows: %d, want %d", n, maxDeviceLog)
	}
}

// Issue #300: the device list says which devices have a live stream and are
// caught up, and every other stream hears when that changes.
func TestDeviceLiveStatus(t *testing.T) {
	s, ts, admin := newTest(t)
	ctx := context.Background()
	phone := mkToken(t, s, "phone", []string{ScopeRead}, nil)
	phoneID := s.lookupToken(ctx, phone).ID
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "m"})

	row := func(ds []LinkedDevice) LinkedDevice {
		for _, d := range ds {
			if d.ID == phoneID {
				return d
			}
		}
		t.Fatalf("phone not in %v", ds)
		return LinkedDevice{}
	}
	pushed := func(m msg) bool { return m.Type == "devices" }

	web := dialWS(t, ts.URL, admin, 0)
	web.until(func(m msg) bool { return m.Type == "ready" })

	p := dialWS(t, ts.URL, phone, 0)
	ready := p.until(func(m msg) bool { return m.Type == "ready" })
	d := row(web.until(func(m msg) bool { return pushed(m) && row(m.Devices).CaughtUp }).Devices)
	if !d.Connected || d.SyncedSeq != ready.Seq || ready.Seq == 0 {
		t.Fatalf("pushed after the phone caught up: %+v, ready seq %d", d, ready.Seq)
	}
	ds, err := s.Devices(ctx, s.lookupToken(ctx, admin))
	if err != nil {
		t.Fatal(err)
	}
	if d := row(ds); !d.Connected || !d.CaughtUp || d.SyncedSeq != ready.Seq {
		t.Fatalf("device list: %+v", d)
	}

	p.c.Close(websocket.StatusNormalClosure, "")
	web.until(func(m msg) bool { return pushed(m) && !row(m.Devices).Connected })

	// A token that cannot see the phone never hears it come and go.
	other := dialWS(t, ts.URL, mkToken(t, s, "other", []string{ScopeRead}, nil), 0)
	other.until(func(m msg) bool { return m.Type == "ready" })
	dialWS(t, ts.URL, phone, 0).until(func(m msg) bool { return m.Type == "ready" })
	call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "infra", "message": "after"})
	if m := other.next(); m.Type == "devices" {
		t.Fatalf("other token told of the phone: %+v", m.Devices)
	}
}

// A second stream on one token closing leaves the token connected.
func TestDeviceStatusTwoStreams(t *testing.T) {
	var h hub
	h.subs = map[chan Event]*stream{}
	one, st := h.subscribe("tok")
	st.ready.Store(true)
	two, _ := h.subscribe("tok") // still replaying
	if c, up, _ := h.status("tok"); !c || !up {
		t.Fatalf("one of two caught up: connected %v caught up %v", c, up)
	}
	h.unsubscribe(one)
	if c, up, _ := h.status("tok"); !c || up {
		t.Fatalf("after the caught-up one closed: connected %v caught up %v", c, up)
	}
	h.unsubscribe(two)
	if c, _, _ := h.status("tok"); c {
		t.Fatal("connected with no stream")
	}
}
