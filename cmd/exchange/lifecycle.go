package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type runningRuntime interface {
	Ready() bool
	Done() <-chan struct{}
	Shutdown(context.Context) error
}
type runningHTTP interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
}

type announcedListener struct {
	net.Listener
	started chan struct{}
	once    sync.Once
}

func (l *announcedListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.started) })
	return l.Listener.Accept()
}

func supervise(ctx context.Context, runtime runningRuntime, server runningHTTP, listener net.Listener, report readyReport, stdout io.Writer, httpTimeout time.Duration) (err error) {
	l := &announcedListener{Listener: listener, started: make(chan struct{})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(l) }()
	var serveErr error
	serveFinished := false
	defer func() {
		// Flush an admitted runtime batch immediately, even if HTTP callers are
		// still waiting for its reply. The HTTP deadline never cancels this drain.
		runtimeDone := make(chan error, 1)
		go func() { runtimeDone <- runtime.Shutdown(context.Background()) }()
		stop, cancel := context.WithTimeout(context.Background(), httpTimeout)
		httpErr := server.Shutdown(stop)
		cancel()
		if httpErr != nil {
			httpErr = errors.Join(fmt.Errorf("HTTP shutdown: %w", httpErr), server.Close())
		}
		runtimeErr := <-runtimeDone
		if !serveFinished {
			serveErr = <-serveDone
		}
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		err = errors.Join(err, httpErr, runtimeErr, serveErr)
	}()
	select {
	case <-l.started:
	case serveErr = <-serveDone:
		serveFinished = true
		return errors.New("HTTP server stopped before startup")
	case <-runtime.Done():
		return errors.New("runtime stopped before HTTP startup")
	case <-ctx.Done():
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	if !runtime.Ready() {
		return errors.New("runtime lost readiness before startup report")
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return fmt.Errorf("write ready report: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case <-runtime.Done():
		return errors.New("runtime stopped while serving") // terminal cause is joined during cleanup
	case serveErr = <-serveDone:
		serveFinished = true
		return errors.New("HTTP server stopped while serving")
	}
}
