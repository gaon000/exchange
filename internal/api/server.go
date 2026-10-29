// Package api adapts the local HTTP/JSON contract to the single-owner runtime.
package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
)

// Backend exposes complete command results and detached views, never an engine.
type Backend interface {
	Ready() bool
	Submit(context.Context, d.Command) (d.Result, error)
	Read(context.Context, rt.Query) (rt.View, error)
}

// Options bounds HTTP resources independently of runtime queue ownership.
// RequestLimit must match the runtime limit in the application composition.
type Options struct {
	RequestLimit      int
	ReadHeaderTimeout time.Duration
	ReadBodyTimeout   time.Duration
	ResultTimeout     time.Duration
	WriteTimeout      time.Duration
}

// DefaultOptions returns the initial v1 transport limits.
func DefaultOptions() Options {
	return Options{16384, 2 * time.Second, 2 * time.Second, 5 * time.Second, 5 * time.Second}
}

// Server serves HTTP/1.1 on a numeric loopback address. It does not own Backend's
// lifetime: the caller must also drain the runtime before exiting the process.
type Server struct {
	backend Backend
	opts    Options
	http    *http.Server
	slots   chan struct{}
	closing atomic.Bool
}

// New constructs the bounded handler and transport without opening a socket.
func New(backend Backend, opts Options) (*Server, error) {
	if backend == nil || opts.RequestLimit < 1 || opts.RequestLimit > 1<<20 {
		return nil, d.ErrInvalid
	}
	for _, timeout := range []time.Duration{opts.ReadHeaderTimeout, opts.ReadBodyTimeout, opts.ResultTimeout, opts.WriteTimeout} {
		if timeout <= 0 || timeout > time.Hour {
			return nil, d.ErrInvalid
		}
	}
	s := &Server{backend: backend, opts: opts, slots: make(chan struct{}, opts.RequestLimit)}
	// Pinned Go 1.27.3 adds 4096 bytes of bufio slack to MaxHeaderBytes.
	// Compensate so the actual request-line/header read ceiling is 8 KiB.
	// Exact wire-boundary tests guard this assumption on toolchain upgrades.
	s.http = &http.Server{Handler: s, MaxHeaderBytes: (8 << 10) - 4096,
		ReadHeaderTimeout: opts.ReadHeaderTimeout, ReadTimeout: opts.ReadHeaderTimeout + opts.ReadBodyTimeout,
		WriteTimeout: opts.ReadBodyTimeout + opts.ResultTimeout + opts.WriteTimeout, IdleTimeout: 30 * time.Second}
	return s, nil
}

// ListenLoopback opens a numeric IPv4/IPv6 loopback TCP listener. It does not
// resolve hostnames or accept wildcard bindings. Port zero supports isolated tests.
func ListenLoopback(address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !loopbackIP(host) || !validPort(port) {
		return nil, fmt.Errorf("listen address must be numeric loopback IP:port")
	}
	return net.Listen("tcp", address)
}

// Serve accepts ownership of a loopback TCP listener and serves until shutdown.
// An invalid listener is rejected without closing it; callers retain ownership.
func (s *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return d.ErrInvalid
	}
	a, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !a.IP.IsLoopback() || a.Zone != "" {
		return fmt.Errorf("listener must bind a loopback TCP address")
	}
	return s.http.Serve(listener)
}

// Shutdown closes admission/listeners and waits for active HTTP handlers.
// Its context does not cancel commands already accepted by the runtime.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closing.Store(true)
	return s.http.Shutdown(ctx)
}

// Close disconnects HTTP clients immediately; accepted runtime work continues.
func (s *Server) Close() error {
	s.closing.Store(true)
	return s.http.Close()
}

func loopbackIP(host string) bool {
	a, err := netip.ParseAddr(host)
	return err == nil && a.Zone() == "" && a.Unmap().IsLoopback()
}
func validPort(port string) bool {
	n, err := d.ParseInt(port)
	return err == nil && n <= 65535
}
func loopbackHost(host string) bool {
	if loopbackIP(host) {
		return true
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return loopbackIP(host[1 : len(host)-1])
	}
	ip, port, err := net.SplitHostPort(host)
	return err == nil && loopbackIP(ip) && validPort(port)
}

// ServeHTTP validates the local transport, acquires a bounded handler slot, and
// holds it through response transmission. It never starts a background handler
// that outlives the request or treats a disconnect as an order cancellation.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A body-free request already has net/http's disconnect reader running.
	// Setting its read deadline would cancel a valid result wait too early.
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.opts.ReadBodyTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			s.fail(w, "REQUEST_TIMEOUT", rt.NotAdmitted)
			return
		}
	}
	if !loopbackHost(r.Host) {
		s.fail(w, "HOST_FORBIDDEN", rt.NotAdmitted)
		return
	}
	if _, present := r.Header["Origin"]; present {
		s.fail(w, "ORIGIN_FORBIDDEN", rt.NotAdmitted)
		return
	}
	if r.ProtoMajor != 1 || r.ProtoMinor != 1 {
		s.fail(w, "PROTOCOL_UNSUPPORTED", rt.NotAdmitted)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		s.fail(w, "TOO_MANY_REQUESTS", rt.NotAdmitted)
		return
	}
	if s.closing.Load() {
		s.fail(w, "NOT_READY", rt.NotAdmitted)
		return
	}
	kind, query, method := route(r.URL.Path)
	if method == "" {
		s.fail(w, "ROUTE_NOT_FOUND", rt.NotAdmitted)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		s.fail(w, "METHOD_NOT_ALLOWED", rt.NotAdmitted)
		return
	}
	values, err := parseQuery(r.URL.RawQuery, &query)
	if err != nil || !values || (query.Kind == rt.OrderQuery || query.Kind == rt.ResultQuery) && !d.ValidID(query.ID) {
		s.fail(w, "INVALID_REQUEST", rt.NotAdmitted)
		return
	}
	if method == http.MethodGet && (r.ContentLength != 0 || len(r.TransferEncoding) > 0) {
		s.fail(w, "INVALID_REQUEST", rt.NotAdmitted)
		return
	}
	if r.URL.Path == "/health/live" {
		s.write(w, http.StatusOK, stateResponse{"LIVE"})
		return
	}
	if r.URL.Path == "/health/ready" {
		if !s.backend.Ready() {
			s.fail(w, "NOT_READY", rt.NotAdmitted)
		} else {
			s.write(w, http.StatusOK, stateResponse{"READY"})
		}
		return
	}
	run, okRun := singleHeader(r, "X-Run-ID")
	account, okAccount := singleHeader(r, "X-Account-ID")
	if !okRun || !okAccount || !d.ValidRunID(run) || !d.ValidID(account) {
		s.fail(w, "INVALID_REQUEST", rt.NotAdmitted)
		return
	}
	if !s.backend.Ready() {
		s.fail(w, "NOT_READY", rt.NotAdmitted)
		return
	}
	if kind != "" {
		c, code := decodeCommand(w, r, kind, run, account)
		if code != "" {
			s.fail(w, code, rt.NotAdmitted)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.opts.ResultTimeout)
		defer cancel()
		result, err := s.backend.Submit(ctx, c)
		if err != nil {
			s.runtimeFailure(w, err)
			return
		}
		s.write(w, http.StatusOK, result)
		return
	}
	query.RunID, query.AccountID = run, account
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.ResultTimeout)
	defer cancel()
	v, err := s.backend.Read(ctx, query)
	if err != nil {
		s.runtimeFailure(w, err)
		return
	}
	s.writeView(w, query, v)
}

func singleHeader(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func (s *Server) runtimeFailure(w http.ResponseWriter, err error) {
	var failure *rt.Failure
	if errors.As(err, &failure) {
		s.fail(w, failure.Code, failure.Outcome)
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// The runtime returns a bare context error only before admission. After
		// admission its typed Failure carries RESULT_TIMEOUT/UNKNOWN.
		s.fail(w, "REQUEST_TIMEOUT", rt.NotAdmitted)
	} else {
		s.fail(w, "INTERNAL_FAILURE", rt.Unknown)
	}
}

func (s *Server) write(w http.ResponseWriter, status int, value any) {
	body, err := encode(value)
	if err != nil {
		status, body = http.StatusServiceUnavailable, []byte(`{"error":{"code":"INTERNAL_FAILURE","outcome":"UNKNOWN"}}`)
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.opts.WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return // no success bytes can safely be delivered on this connection
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	if _, err := w.Write(body); err == nil {
		// net/http buffers small responses until after ServeHTTP returns. Flush
		// here so the handler slot remains held through the final socket write.
		_ = http.NewResponseController(w).Flush()
	} // a failed send never changes the persisted command result
}
