package gzfs

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/fstest"
)

func TestAcceptsGzip(t *testing.T) {
	for h, want := range map[string]bool{
		"gzip": true, "GZIP": true, "br, gzip;q=0.5": true, "gzip, deflate, br": true, "gzip;q=1": true,
		"": false, "deflate": false, "gzip;q=0": false, "gzip; q=0.0": false, "gzip;q=0.000": false, "x-gzip2": false, "gzip;q=x": false,
	} {
		if AcceptsGzip(h) != want {
			t.Errorf("%q: want %v", h, want)
		}
	}
}

// Serve reads every Accept-Encoding line, sends the gzip length on a 200 and
// no encoding or length on a 412 (#690 review).
func TestServe(t *testing.T) {
	var gz bytes.Buffer
	z := gzip.NewWriter(&gz)
	z.Write([]byte("body { color: red }"))
	z.Close()
	f := New(fstest.MapFS{"p/a.css.gz": {Data: gz.Bytes()}}, "p", nil)
	get := func(h http.Header) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/a.css", nil)
		r.Header = h
		w := httptest.NewRecorder()
		w.Header().Set("ETag", `"x"`)
		if !Serve(w, r, f, "a.css") {
			w.Code = 0
		}
		return w
	}
	w := get(http.Header{"Accept-Encoding": {"br", "gzip"}})
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || !bytes.Equal(w.Body.Bytes(), gz.Bytes()) ||
		w.Header().Get("Content-Length") != strconv.Itoa(gz.Len()) || w.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("gzip: %d %v", w.Code, w.Header())
	}
	w = get(http.Header{"Accept-Encoding": {"gzip"}, "If-Match": {`"old"`}})
	if w.Code != 412 || w.Header().Get("Content-Encoding") != "" || w.Header().Get("Content-Length") != "" {
		t.Errorf("412: %d %v", w.Code, w.Header())
	}
}
