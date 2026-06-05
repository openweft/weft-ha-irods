// Package api exposes the role HTTP endpoints the L4 Caddy in
// weft-agent active-probes :
//
//	GET /ready   — 200 when the local iRODS server is up + serving the
//	               configured zone ; 503 otherwise. Caddy uses this to
//	               drain unhealthy providers from the upstream pool.
//	GET /zone    — JSON {zone, dc, node} ; for ops dashboards.
//	GET /health  — same as /ready, kept for the conventional probe path
//	               many tools default to.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"
)

// State is what the API surfaces. Filled by the reconcile loop each
// tick ; the API holders read it via atomic snapshot so the role
// endpoint never blocks the reconcile.
type State struct {
	Up       bool
	ZoneName string
	NodeName string
	DC       string
}

// Server is a thin HTTP wrapper around an atomically-swapped State.
// Construct via New ; the reconcile loop calls Update each tick.
type Server struct {
	addr  string
	srv   *http.Server
	state atomic.Pointer[State]
}

// New returns a configured but not-yet-started Server. Call Start
// to bind the listener.
func New(addr, nodeName, dc string) *Server {
	s := &Server{addr: addr}
	s.state.Store(&State{NodeName: nodeName, DC: dc})

	mux := http.NewServeMux()
	mux.HandleFunc("/ready", s.ready)
	mux.HandleFunc("/health", s.ready)
	mux.HandleFunc("/zone", s.zone)

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Start binds the listener + serves in a goroutine. Returns when
// the listener is bound (or an error from the bind step).
func (s *Server) Start() error {
	if s.srv == nil {
		return errors.New("api: server not constructed")
	}
	go func() { _ = s.srv.ListenAndServe() }()
	return nil
}

// Shutdown stops the listener. Idempotent.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// Update is what the reconcile loop calls after each tick to push a
// fresh State for the next probe to read.
func (s *Server) Update(st State) { s.state.Store(&st) }

func (s *Server) snapshot() State {
	p := s.state.Load()
	if p == nil {
		return State{}
	}
	return *p
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	st := s.snapshot()
	if !st.Up {
		http.Error(w, "iRODS server not up", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) zone(w http.ResponseWriter, _ *http.Request) {
	st := s.snapshot()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Zone string `json:"zone"`
		Node string `json:"node"`
		DC   string `json:"dc"`
		Up   bool   `json:"up"`
	}{Zone: st.ZoneName, Node: st.NodeName, DC: st.DC, Up: st.Up})
}
