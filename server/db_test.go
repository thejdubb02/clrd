package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A query whose context ends just as its first row arrives must not leave a
// read transaction open on its pooled connection. Such a connection serves
// old data to later reads and fails later writes with "database is locked
// (517)" (#207). The web inbox hits this when a page reload cancels its
// snapshot request.
func TestCancelledQueryLeavesNoOpenSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // every query reuses the one connection
	other, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := db.Exec(`CREATE TABLE n(v INTEGER); INSERT INTO n VALUES(0)`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for i := 1; time.Now().Before(deadline); i++ {
		ctx, cancel := context.WithCancel(context.Background())
		// Cancel at varying points around the first step.
		time.AfterFunc(time.Duration(i%40)*time.Microsecond, cancel)
		if rows, err := db.QueryContext(ctx, `SELECT v FROM n`); err == nil {
			rows.Close()
		}
		cancel()
		if _, err := other.Exec(`UPDATE n SET v = ?`, i); err != nil {
			t.Fatal(err)
		}
		var v int
		if err := db.QueryRow(`SELECT v FROM n`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != i {
			t.Fatalf("after %d cancelled queries a read saw %d, want %d: a cancelled query left its snapshot open", i, v, i)
		}
	}
}
