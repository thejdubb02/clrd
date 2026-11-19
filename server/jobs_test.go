package server

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// A background job that keeps failing turns /healthz red (so the Docker
// health check and monitors notice), and green again once it recovers.
func TestHealthzReportsFailingJob(t *testing.T) {
	s, ts, _ := newTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var broken atomic.Bool
	broken.Store(true)
	go s.every(ctx, "snooze wake", time.Millisecond, func(context.Context) error {
		if broken.Load() {
			return errors.New("disk full")
		}
		return nil
	})
	waitHealth := func(want int) map[string]any {
		t.Helper()
		for start := time.Now(); time.Since(start) < 5*time.Second; time.Sleep(5 * time.Millisecond) {
			if code, body := call(t, ts, "GET", "/healthz", "", nil); code == want {
				return body
			}
		}
		t.Fatalf("healthz never answered %d", want)
		return nil
	}
	body := waitHealth(503)
	if body["ok"] != false || fmt.Sprint(body["failing"]) != "[snooze wake]" {
		t.Fatalf("healthz while failing: %v", body)
	}
	broken.Store(false)
	waitHealth(200)

	// One failure alone is not enough to go red: an hourly job fails once.
	var once atomic.Int64
	go s.every(ctx, "prune", time.Hour, func(context.Context) error { once.Add(1); return errors.New("busy") })
	for once.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond) // past the Store a wrong threshold would make
	if code, body := call(t, ts, "GET", "/healthz", "", nil); code != 200 {
		t.Fatalf("one failure turned healthz red: %v", body)
	}
}
