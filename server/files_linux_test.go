package server

import (
	"bytes"
	"strings"
	"syscall"
	"testing"
)

// A disk that fills mid-upload is the server's fault, not the sender's: a
// 507 the sender may retry, without the server's folder in the message.
func TestUploadDiskFull(t *testing.T) {
	s, ts, admin := newTest(t)
	var was syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &was); err != nil {
		t.Skip(err)
	}
	// Go ignores SIGXFSZ, so a write past the limit fails as a full disk would.
	lim := was
	lim.Cur = 1 << 20
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &lim); err != nil {
		t.Skip(err)
	}
	code, out := upload(t, ts, admin, map[string]any{"topic": "infra"}, "big.bin", bytes.Repeat([]byte("x"), 2<<20), false)
	syscall.Setrlimit(syscall.RLIMIT_FSIZE, &was)
	if msg, _ := out["error"].(string); code != 507 || strings.Contains(msg, s.opt.Attachments) {
		t.Fatalf("disk full: %d %v", code, out)
	}
	if f := files(t, s); len(f) != 0 {
		t.Fatalf("left files: %v", f)
	}
}
