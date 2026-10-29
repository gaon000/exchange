package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
)

func startHTTP(t *testing.T, backend Backend, options Options) (*Server, string, *http.Client) {
	t.Helper()
	l, err := ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return startHTTPListener(t, backend, options, l)
}

func startHTTPListener(t *testing.T, backend Backend, options Options, l net.Listener) (*Server, string, *http.Client) {
	t.Helper()
	s, err := New(backend, options)
	if err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	transport := &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 64}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		_ = s.Close()
		if err := receive(t, done); !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve: %v", err)
		}
	})
	return s, "http://" + l.Addr().String(), client
}

type smallSendBuffer struct {
	net.Listener
	writes chan error
}

func (l smallSendBuffer) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if err := c.(*net.TCPConn).SetWriteBuffer(1024); err != nil {
		_ = c.Close()
		return nil, err
	}
	return observedWrites{c, l.writes}, nil
}

type observedWrites struct {
	net.Conn
	writes chan error
}

func (c observedWrites) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		select {
		case c.writes <- err:
		default:
		}
	}
	return n, err
}

func TestNetworkSlowReaderHitsWriteDeadlineAndReleasesSlot(t *testing.T) {
	b := &stubBackend{}
	b.ready.Store(true)
	entered := make(chan struct{})
	b.read = func(_ context.Context, q rt.Query) (rt.View, error) {
		v := rt.View{RunID: testRun, AsOfSeq: 1000, ThroughSeq: 1000}
		for i := range 1000 {
			v.Trades = append(v.Trades, d.Trade{TradeID: fmt.Sprintf("%s:%d:1", testRun, i+1), CommandSeq: d.Int(i + 1), MatchIndex: 1, PriceTicks: 100, QuantityLots: 1, TakerSide: d.Buy})
		}
		close(entered)
		return v, nil
	}
	l, err := ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errorsSeen := make(chan error, 4)
	o := DefaultOptions()
	o.RequestLimit = 1
	o.WriteTimeout = 200 * time.Millisecond
	s, base, client := startHTTPListener(t, b, o, smallSendBuffer{l, errorsSeen})
	c := rawConnection(t, base)
	if err := c.(*net.TCPConn).SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(c, "GET /v1/trades?limit=1000 HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Run-ID: %s\r\nX-Account-ID: alice\r\n\r\n", testRun)
	if err != nil {
		t.Fatal(err)
	}
	receive(t, entered)
	status, data := call(t, client, base, testRun, "alice", "GET", "/health/live", "")
	if status != 429 {
		t.Fatalf("write slot not retained: %d %s", status, data)
	}
	err = receive(t, errorsSeen)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("expected write deadline: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for len(s.slots) != 0 && time.Now().Before(deadline) {
		<-time.After(time.Millisecond)
	}
	if len(s.slots) != 0 {
		t.Fatal("slow reader leaked slot")
	}
	status, data = call(t, client, base, testRun, "alice", "GET", "/health/live", "")
	if status != 200 {
		t.Fatalf("server stopped after slow reader: %d %s", status, data)
	}
}

func call(t *testing.T, client *http.Client, base, run, account, method, path, body string) (int, []byte) {
	t.Helper()
	r, err := http.NewRequest(method, base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Run-ID", run)
	r.Header.Set("X-Account-ID", account)
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("read/close: %v %v", err, closeErr)
	}
	return response.StatusCode, data
}

func rawConnection(t *testing.T, base string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(base, "http://"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNetworkHeaderAndBodyBoundaries(t *testing.T) {
	o := DefaultOptions()
	o.ReadHeaderTimeout = 50 * time.Millisecond
	o.ReadBodyTimeout = 50 * time.Millisecond
	b := &stubBackend{}
	b.ready.Store(true)
	_, base, client := startHTTP(t, b, o)
	t.Run("header timeout", func(t *testing.T) {
		c := rawConnection(t, base)
		_, err := io.WriteString(c, "GET /health/live HTTP/1.1\r\nHost: ")
		if err != nil {
			t.Fatal(err)
		}
		_, err = bufio.NewReader(c).ReadByte()
		if err == nil {
			t.Fatal("incomplete header unexpectedly accepted")
		}
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			t.Fatal("server did not enforce header deadline")
		}
	})
	t.Run("header limit", func(t *testing.T) {
		c := rawConnection(t, base)
		_, err := fmt.Fprintf(c, "GET /health/live HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Large: %s\r\n\r\n", strings.Repeat("a", 20<<10))
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 431 {
			t.Fatal(response.Status)
		}
	})
	for _, size := range []int{8192, 8193} {
		t.Run(fmt.Sprintf("exact-header-%d", size), func(t *testing.T) {
			c := rawConnection(t, base)
			prefix := "GET /health/live HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Pad: "
			wire := prefix + strings.Repeat("a", size-len(prefix)-4) + "\r\n\r\n"
			if _, err := io.WriteString(c, wire); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			want := 200
			if size > 8192 {
				want = 431
			}
			if response.StatusCode != want {
				t.Fatalf("header %d: %s", size, response.Status)
			}
		})
	}
	t.Run("body timeout before admission", func(t *testing.T) {
		c := rawConnection(t, base)
		_, err := fmt.Fprintf(c, "POST /v1/orders HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Run-ID: %s\r\nX-Account-ID: alice\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{", testRun)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 408 || !bytes.Contains(data, []byte(`"outcome":"NOT_ADMITTED"`)) {
			t.Fatalf("%d %s %v", response.StatusCode, data, err)
		}
	})
	for _, size := range []int{4096, 4097} {
		t.Run(fmt.Sprintf("chunked-%d", size), func(t *testing.T) {
			body := placeBody + strings.Repeat(" ", size-len(placeBody))
			r, err := http.NewRequest("POST", base+"/v1/orders", io.NopCloser(strings.NewReader(body)))
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("X-Run-ID", testRun)
			r.Header.Set("X-Account-ID", "alice")
			r.Header.Set("Content-Type", "application/json")
			response, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			want := 200
			if size > 4096 {
				want = 413
			}
			if response.StatusCode != want {
				t.Fatal(response.Status)
			}
		})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.input) != 1 {
		t.Fatal("invalid body/header reached runtime", len(b.input))
	}
}

func TestNetworkTimeoutDisconnectAndHTTPAdmission(t *testing.T) {
	for _, mode := range []string{"timeout", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			o := DefaultOptions()
			o.ResultTimeout = time.Second
			o.RequestLimit = 1
			entered, finished := make(chan struct{}), make(chan struct{})
			b := &stubBackend{}
			b.ready.Store(true)
			b.submit = func(ctx context.Context, c d.Command) (d.Result, error) {
				close(entered)
				<-ctx.Done()
				close(finished)
				return d.Result{}, &rt.Failure{Code: "RESULT_TIMEOUT", Outcome: rt.Unknown, Cause: ctx.Err()}
			}
			_, base, client := startHTTP(t, b, o)
			c := rawConnection(t, base)
			_, err := fmt.Fprintf(c, "POST /v1/orders HTTP/1.1\r\nHost: 127.0.0.1\r\nX-Run-ID: %s\r\nX-Account-ID: alice\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", testRun, len(placeBody), placeBody)
			if err != nil {
				t.Fatal(err)
			}
			receive(t, entered)
			status, body := call(t, client, base, testRun, "alice", "GET", "/v1/balances", "")
			if status != 429 || !bytes.Contains(body, []byte("TOO_MANY_REQUESTS")) {
				t.Fatalf("slot admission: %d %s", status, body)
			}
			if mode == "disconnect" {
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				response, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if err != nil || response.StatusCode != 504 || !bytes.Contains(body, []byte(`"outcome":"UNKNOWN"`)) {
					t.Fatalf("%d %s %v", response.StatusCode, body, err)
				}
			}
			receive(t, finished)
		})
	}
}

func TestNetworkBodyDeadlineDoesNotShortenQueryWait(t *testing.T) {
	o := DefaultOptions()
	o.ReadBodyTimeout, o.ResultTimeout = 10*time.Millisecond, time.Second
	entered, release := make(chan struct{}), make(chan struct{})
	b := &stubBackend{}
	b.ready.Store(true)
	b.read = func(ctx context.Context, q rt.Query) (rt.View, error) {
		close(entered)
		select {
		case <-release:
			return rt.View{RunID: testRun}, nil
		case <-ctx.Done():
			return rt.View{}, &rt.Failure{Code: "RESULT_TIMEOUT", Outcome: rt.Unknown, Cause: ctx.Err()}
		}
	}
	_, base, client := startHTTP(t, b, o)
	r, err := http.NewRequest("GET", base+"/v1/balances", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("X-Run-ID", testRun)
	r.Header.Set("X-Account-ID", "alice")
	type result struct {
		response *http.Response
		err      error
	}
	done := make(chan result, 1)
	go func() { response, err := client.Do(r); done <- result{response, err} }()
	receive(t, entered)
	// Cross the body deadline, while remaining well within result timeout.
	<-time.After(50 * time.Millisecond)
	close(release)
	got := receive(t, done)
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.response.Body.Close()
	if got.response.StatusCode != 200 {
		t.Fatal(got.response.Status)
	}
}
