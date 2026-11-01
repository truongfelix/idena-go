package main

import (
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/idena-network/idena-go/log"
	"github.com/pkg/errors"
)

// pprofHost is fixed: the profiles show the node's memory and goroutines, and are served to this computer only.
const pprofHost = "127.0.0.1"

// pprofMux serves the net/http/pprof handlers on a mux of its own, not on http.DefaultServeMux.
func pprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// startPprofServer listens on 127.0.0.1:port (port 0: any free port) and serves the profiles in the background.
// It returns the address it listens on.
func startPprofServer(port int) (string, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(pprofHost, strconv.Itoa(port)))
	if err != nil {
		return "", errors.Wrap(err, "cannot serve the Go runtime profiles")
	}
	server := &http.Server{
		Handler:           pprofMux(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("Go runtime profiles server stopped", "err", err)
		}
	}()
	return listener.Addr().String(), nil
}
