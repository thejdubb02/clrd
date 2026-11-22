package server

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarks(t *testing.T) {
	s, ts, admin := newTest(t)
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "triage", "message": "disk 91%",
		"actions": []any{map[string]any{"label": "ok", "reply": "ok"}}})
	id := n["id"].(string)
	path := "/api/v1/notifications/" + id + "/mark"
	stew := mkToken(t, s, "stew", []string{ScopeRead}, []string{"triage"})
	marks := func(n map[string]any) []any { m, _ := n["marks"].([]any); return m }

	if marks(n) == nil {
		t.Fatalf("a new notification has no marks list: %v", n)
	}
	for name, c := range map[string]struct {
		tok  string
		body map[string]any
		want int
	}{
		"publish only": {mkToken(t, s, "sender", []string{ScopePublish}, nil), map[string]any{"mark": "seen"}, 403},
		"other topic":  {mkToken(t, s, "elsewhere", []string{ScopeRead}, []string{"infra"}), map[string]any{"mark": "seen"}, 403},
		"no token":     {"", map[string]any{"mark": "seen"}, 401},
		"bad kind":     {stew, map[string]any{"mark": "done"}, 400},
		"no kind":      {stew, map[string]any{}, 400},
		"long note":    {stew, map[string]any{"mark": "actioned", "note": strings.Repeat("a", 201)}, 400},
		"script link":  {stew, map[string]any{"mark": "actioned", "link": "javascript:alert(1)"}, 400},
	} {
		if code, _ := call(t, ts, "POST", path, c.tok, c.body); code != c.want {
			t.Errorf("%s: want %d, got %d", name, c.want, code)
		}
	}
	if code, _ := call(t, ts, "POST", "/api/v1/notifications/nope/mark", stew, map[string]any{"mark": "seen"}); code != 404 {
		t.Errorf("unknown id: want 404, got %d", code)
	}

	seq := s.CurrentSeq()
	body := map[string]any{"mark": "actioned", "note": "filed relay#512", "link": "https://github.com/x/relay/issues/512"}
	code, n := call(t, ts, "POST", path, stew, body)
	if code != 200 || len(marks(n)) != 1 || n["state"] != "active" {
		t.Fatalf("mark: %d %v", code, n)
	}
	m := marks(n)[0].(map[string]any)
	if m["by"] != "stew" || m["mark"] != "actioned" || m["note"] != "filed relay#512" || m["link"] != body["link"] || m["at"] == "" {
		t.Fatalf("mark: %v", m)
	}
	if s.CurrentSeq() != seq+1 {
		t.Fatalf("mark logged %d events, want 1", s.CurrentSeq()-seq)
	}
	if call(t, ts, "POST", path, stew, body); s.CurrentSeq() != seq+1 {
		t.Error("the same mark again logged an event")
	}

	// The owner sees it, in the list too, and adds their own beside it.
	_, l := call(t, ts, "GET", "/api/v1/notifications?topic=triage", admin, nil)
	if items, _ := l["notifications"].([]any); len(items) != 1 || len(marks(items[0].(map[string]any))) != 1 {
		t.Fatalf("list: %v", l)
	}
	if _, n := call(t, ts, "POST", path, admin, map[string]any{"mark": "seen"}); len(marks(n)) != 2 {
		t.Fatalf("second author: %v", n)
	}
	// Only the author changes or removes a mark: stew's DELETE takes only his.
	if _, n := call(t, ts, "DELETE", path, stew, nil); len(marks(n)) != 1 || marks(n)[0].(map[string]any)["by"] != "admin" {
		t.Fatalf("unmark: %v", n)
	}
	call(t, ts, "POST", path, stew, map[string]any{"mark": "seen"})

	// A mark is not an answer: a tap showing the seq from before it still counts.
	seq = s.CurrentSeq()
	call(t, ts, "POST", path, stew, body)
	if code, n := call(t, ts, "POST", "/api/v1/notifications/"+id+"/reply", mkToken(t, s, "phone", []string{ScopeRead}, nil), map[string]any{"reply": "ok", "seq": seq}); code != 200 {
		t.Fatalf("reply after a mark: %d %v", code, n)
	}

	// ntfy has no marks, so an ntfy client gets the message once, not per mark.
	if code, out := raw(t, "GET", ts.URL+"/triage/json?poll=1", admin, "", nil); code != 200 || len(lines(out)) != 2 { // created, then the reply's state
		t.Fatalf("ntfy poll: %d %q", code, out)
	}
	call(t, ts, "POST", path, admin, map[string]any{"mark": "actioned"}) // now the newest event is a mark
	if _, out := raw(t, "GET", ts.URL+"/triage/json?poll=1&since=latest", admin, "", nil); out == "" || len(lines(out)) != 1 {
		t.Fatalf("since=latest after a mark: %q", out)
	}
}

// A person's devices share one mark, named for the person.
func TestMarksByPerson(t *testing.T) {
	s, ts, admin := newTest(t)
	_, u := call(t, ts, "POST", "/api/v1/users", admin, map[string]any{"name": "Ann"})
	phone, tablet := mkToken(t, s, "ann-phone", []string{ScopeRead}, nil), mkToken(t, s, "ann-tablet", []string{ScopeRead}, nil)
	for _, tok := range []string{phone, tablet} {
		var tid string
		s.db.QueryRow(`SELECT id FROM tokens WHERE hash = ?`, hashToken(tok)).Scan(&tid)
		if c, _ := call(t, ts, "PUT", "/api/v1/tokens/"+tid+"/user", admin, map[string]any{"user": u["id"]}); c != 200 {
			t.Fatalf("attach: %d", c)
		}
	}
	_, n := call(t, ts, "POST", "/api/v1/notifications", admin, map[string]any{"topic": "family", "message": "dinner"})
	path := "/api/v1/notifications/" + n["id"].(string) + "/mark"
	call(t, ts, "POST", path, phone, map[string]any{"mark": "seen"})
	_, n = call(t, ts, "POST", path, tablet, map[string]any{"mark": "actioned"})
	ms := n["marks"].([]any)
	if len(ms) != 1 || ms[0].(map[string]any)["by"] != "Ann" || ms[0].(map[string]any)["by_id"] != u["id"] || ms[0].(map[string]any)["mark"] != "actioned" {
		t.Fatalf("person mark: %v", ms)
	}
}

func TestMarksSurviveRestart(t *testing.T) {
	db := filepath.Join(t.TempDir(), "clrd.db")
	open := func() *Server {
		s, err := New(Options{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	tok, _, err := s.CreateToken(t.Context(), "stew", "allowed", []string{ScopeAdmin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.Publish(t.Context(), tok, PublishInput{Topic: "triage", Message: "disk"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMark(t.Context(), tok, n.ID, MarkActioned, "filed", ""); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open()
	defer s.Close()
	got, err := s.Get(t.Context(), tok, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Marks) != 1 || got.Marks[0].By != "stew" || got.Marks[0].Note != "filed" {
		t.Fatalf("after restart: %+v", got.Marks)
	}
}

// #452: a database from before qseq fills it from the log on start: the last
// change to the question, not a later mark; the row's seq when that is pruned.
func TestQseqFilled(t *testing.T) {
	db := filepath.Join(t.TempDir(), "clrd.db")
	open := func() *Server {
		s, err := New(Options{DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	tok, _, _ := s.CreateToken(t.Context(), "stew", "allowed", []string{ScopeAdmin}, nil)
	a, _ := s.Publish(t.Context(), tok, PublishInput{Topic: "triage", Message: "a"})
	b, _ := s.Publish(t.Context(), tok, PublishInput{Topic: "triage", Message: "b"})
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.SetMark(t.Context(), tok, id, MarkSeen, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`DELETE FROM events WHERE seq = ?`, b.Seq); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE notifications DROP COLUMN qseq`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open()
	defer s.Close()
	q := func(id string) (q, seq int64) {
		s.db.QueryRow(`SELECT qseq, seq FROM notifications WHERE id = ?`, id).Scan(&q, &seq)
		return
	}
	if got, _ := q(a.ID); got != a.Seq {
		t.Errorf("a: qseq %d, want its publish %d", got, a.Seq)
	}
	if got, seq := q(b.ID); got != seq || seq == b.Seq {
		t.Errorf("b: qseq %d, want its row seq %d", got, seq)
	}
}
