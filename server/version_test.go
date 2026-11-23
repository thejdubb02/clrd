package server

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/thejdubb02/clrd"
)

// The version needs no token and is only what the binary carries (#580).
func TestVersionEndpoint(t *testing.T) {
	_, ts, _ := newTest(t)
	res, err := http.Get(ts.URL + "/api/v1/version")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/json" || !bytes.Equal(b, clrd.VersionJSON()) {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
}
