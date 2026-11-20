package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDeviceSound(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx := context.Background()
	typed := mkToken(t, s, "laptop", []string{ScopeRead}, nil)
	phone := linkPhone(t, ts, typed)
	phoneID := s.lookupToken(ctx, phone).ID
	other := mkToken(t, s, "other", []string{ScopeRead}, nil)
	limited := mkToken(t, s, "limited", []string{ScopeRead}, []string{"build"})

	if code, out := call(t, ts, "GET", "/api/v1/sounds", other, nil); code != 200 || out["default"] != "default" ||
		!strings.Contains(fmt.Sprint(out["sounds"]), "chime") || !strings.Contains(fmt.Sprint(out["sounds"]), "none") {
		t.Fatalf("sounds: %d %v", code, out)
	}
	soundIn := func(tok string) (settings, listed any) {
		t.Helper()
		_, st := call(t, ts, "GET", "/api/v1/settings", tok, nil)
		_, out := call(t, ts, "GET", "/api/v1/devices?all=1", typed, nil)
		for _, d := range out["devices"].([]any) {
			if d.(map[string]any)["id"] == phoneID {
				listed = d.(map[string]any)["sound"]
			}
		}
		return st["sound"], listed
	}
	if st, l := soundIn(phone); st != "default" || l != "default" {
		t.Fatalf("out of the box: settings %v, list %v", st, l)
	}

	// The laptop sets the phone's sound; the phone sees it, the laptop does not change.
	if code, out := call(t, ts, "PUT", "/api/v1/devices/"+phoneID, typed, map[string]any{"sound": "chime"}); code != 200 || out["sound"] != "chime" {
		t.Fatalf("set: %d %v", code, out)
	}
	if st, l := soundIn(phone); st != "chime" || l != "chime" {
		t.Fatalf("after set: settings %v, list %v", st, l)
	}
	if st, _ := soundIn(typed); st != "default" {
		t.Fatalf("one device's sound leaked to another: %v", st)
	}

	// Unknown name, someone else's device, a topic-limited token.
	if code, out := call(t, ts, "PUT", "/api/v1/devices/"+phoneID, typed, map[string]any{"sound": "kazoo"}); code != 400 ||
		!strings.Contains(fmt.Sprint(out), "chime") {
		t.Fatalf("unknown sound: %d %v", code, out)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+phoneID, other, map[string]any{"sound": "pop"}); code != 404 {
		t.Fatalf("someone else's device: %d", code)
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/devices/"+phoneID, limited, map[string]any{"sound": "pop"}); code != 403 {
		t.Fatalf("limited token: %d", code)
	}
	if st, _ := soundIn(phone); st != "chime" {
		t.Fatalf("a refused change still changed it: %v", st)
	}

	// The device sets its own through settings, and back to the default.
	for _, v := range []string{"none", "default"} {
		if code, out := call(t, ts, "PUT", "/api/v1/settings", phone, map[string]any{"sound": v}); code != 200 || out["sound"] != v {
			t.Fatalf("settings sound %s: %d %v", v, code, out)
		}
	}
	if code, _ := call(t, ts, "PUT", "/api/v1/settings", phone, map[string]any{"sound": "kazoo"}); code != 400 {
		t.Fatalf("settings unknown sound: %d", code)
	}
}
