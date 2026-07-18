// Command p4watchd is the p4watch daemon.
//
// It keeps a cached answer to one question ("what in this workspace differs from
// the server?") and serves it over a local HTTP endpoint in O(1). This file is
// only wiring: parse flags, build a daemon.Server, start its re-anchor loop, and
// serve. The behavior (the honest cached state and the endpoint contracts) lives
// in package daemon.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/Aadil101/p4watch/internal/daemon"
	"github.com/Aadil101/p4watch/internal/p4"
)

func main() {
	var (
		root     = flag.String("root", `C:\p4bench\ws`, "local workspace root")
		port     = flag.String("p4port", "localhost:1666", "P4PORT")
		user     = flag.String("p4user", "bench", "P4USER")
		client   = flag.String("p4client", "bench_ws", "P4CLIENT (workspace name)")
		p4bin    = flag.String("p4bin", "p4", "path to the p4 executable")
		addr     = flag.String("addr", "127.0.0.1:7778", "status endpoint address")
		backstop = flag.Duration("backstop", 2*time.Minute, "periodic re-anchor interval (missed-event safety net)")
		debounce = flag.Duration("debounce", 500*time.Millisecond, "quiet window after a file event before re-anchoring")
	)
	flag.Parse()

	c := &p4.Client{Port: *port, User: *user, Client: *client, Root: *root, Bin: *p4bin}
	srv := daemon.New(c, daemon.Config{Backstop: *backstop, Debounce: *debounce})
	go srv.Run(context.Background())

	mux := http.NewServeMux()
	srv.Register(mux)

	log.Printf("p4watchd watching %s via %s", *root, *port)
	log.Printf("endpoints: http://%s/summary  /status  /healthz", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
