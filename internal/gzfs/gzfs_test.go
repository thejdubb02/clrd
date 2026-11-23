package gzfs

import "testing"

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
