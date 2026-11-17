package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntermediaryCloseWaitsForDisconnectedHandler(t *testing.T) {
	entered, exit, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(exit) }) }
	url, stop := startLossIntermediary(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
		} else if err := conn.Close(); err != nil {
			t.Error(err)
		}
		close(entered)
		<-exit // Simulates the held original after the client has lost its response.
		close(finished)
	}))
	defer func() { release(); stop() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("response was not lost")
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("handler did not start")
	}
	closing, stopped := make(chan struct{}), make(chan struct{})
	go func() { close(closing); stop(); close(stopped) }()
	<-closing
	select {
	case <-stopped:
		t.Fatal("close returned with a disconnected handler still running")
	case <-time.After(100 * time.Millisecond):
	}
	release() // Error/cancellation cleanup must unblock, then join, the handler.
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("close did not join the handler")
	}
	select {
	case <-finished:
	default:
		t.Fatal("handler outlived intermediary")
	}
}
