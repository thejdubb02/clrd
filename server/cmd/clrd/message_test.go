package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thejdubb02/clrd/server"
)

func TestMessageCommands(t *testing.T) {
	_, ts, admin := realServer(t)
	t.Setenv("CLRD_URL", ts.URL+"/")
	t.Setenv("CLRD_TOKEN", admin)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run := func(cmd string, out io.Writer, args ...string) error {
		return messageCmd(ctx, server.Options{}, cmd, args, out)
	}
	type event struct {
		Event, Topic, Title, Message string
		Priority                     int
		Tags                         []string
	}
	next := func(dec *json.Decoder) event {
		t.Helper()
		var e event
		if err := dec.Decode(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	var out bytes.Buffer
	if err := run("publish", &out, "--title", "Disk", "--priority", "high", "--tags", "ops,urgent", "message", "Disk", "full"); err != nil {
		t.Fatal(err)
	}
	if e := next(json.NewDecoder(&out)); e.Topic != "message" || e.Message != "Disk full" || e.Title != "Disk" ||
		e.Priority != 4 || strings.Join(e.Tags, ",") != "ops,urgent" {
		t.Fatalf("publish reply: %+v", e)
	}
	out.Reset()
	if err := run("poll", &out, "--since", "all", "message,other"); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	if e := next(dec); e.Event != "message" || e.Message != "Disk full" {
		t.Fatalf("poll reply: %+v", e)
	}
	var e event
	if err := dec.Decode(&e); err != io.EOF {
		t.Fatalf("poll did not finish after the stored message: %+v %v", e, err)
	}

	r, w := io.Pipe()
	defer r.Close()
	done := make(chan error, 1)
	go func() {
		err := run("subscribe", w, "message")
		w.CloseWithError(err)
		done <- err
	}()
	dec = json.NewDecoder(r)
	if e := next(dec); e.Event != "open" {
		t.Fatalf("subscribe did not open: %+v", e)
	}
	if err := run("publish", io.Discard, "message", "A live alert"); err != nil {
		t.Fatal(err)
	}
	if e := next(dec); e.Event != "message" || e.Message != "A live alert" {
		t.Fatalf("subscribe reply: %+v", e)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("subscribe on cancellation: %v", err)
	}
}
