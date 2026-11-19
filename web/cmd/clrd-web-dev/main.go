// Command clrd-web-dev serves the web UI against an in-memory fake server, so
// the pages can be worked on without the real backend.
//
//	go run ./web/cmd/clrd-web-dev -addr 127.0.0.1:8080
//
// Publish into it with curl; X-Device sets who acted:
//
//	curl -d '{"topic":"test","title":"Hello"}' localhost:8080/api/v1/notifications
//	curl -X POST -H 'X-Device: pixel-9' localhost:8080/api/v1/notifications/<id>/dismiss
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/thejdubb02/clrd/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()
	fake := web.NewFake()
	mux := http.NewServeMux()
	fake.Register(mux)
	mux.Handle("GET /", web.Handler(fake, ""))
	log.Printf("clrd web dev server on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
