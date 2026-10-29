//go:build darwin && cgo

package runtime_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/wal"
)

type beforeHTTPResponse struct {
	*rt.Runtime
	boundary func()
}

func (b beforeHTTPResponse) Submit(ctx context.Context, c d.Command) (d.Result, error) {
	result, err := b.Runtime.Submit(ctx, c)
	if err == nil {
		b.boundary()
	}
	return result, err
}

type partialHTTPListener struct {
	net.Listener
	boundary func()
}

func (l partialHTTPListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return partialHTTPConn{c, l.boundary}, nil
}

type partialHTTPConn struct {
	net.Conn
	boundary func()
}

func (c partialHTTPConn) Write(data []byte) (int, error) {
	// The fixture response fits in net/http's flush buffer. Observe actual wire
	// bytes and forward a strict prefix of its body before the parent kills us.
	if bytes.Contains(data, []byte(`"requestId":"target"`)) {
		header := bytes.Index(data, []byte("\r\n\r\n"))
		if header < 0 || len(data)-header-4 < 2 {
			return 0, errors.New("missing expected response frame")
		}
		prefix := header + 4 + (len(data)-header-4)/2
		n, err := c.Conn.Write(data[:prefix])
		if err != nil || n != prefix {
			return n, err
		}
		c.boundary()
		return n, errors.New("process-kill boundary unexpectedly returned")
	}
	return c.Conn.Write(data)
}

func runHTTPBoundaryChild(t *testing.T, dir, phase string) {
	t.Helper()
	boundary := func() {
		fmt.Fprintln(os.Stdout, "BOUNDARY "+phase)
		select {} // Parent kills only this owned child after observing the marker.
	}
	h := rt.HTTPTestHooks{}
	switch phase {
	case "before-write", "partial-write", "before-sync", "after-sync":
		group, err := os.ReadFile(os.Getenv("EXCHANGE_HTTP_FAULT_GROUP"))
		if err != nil {
			t.Fatal(err)
		}
		h.Append = func(_ *wal.Store, records []wal.Record) error {
			if len(records) != 1 || records[0].Seq != 2 {
				return errors.New("unexpected kill fixture batch")
			}
			if err := writeHTTPFaultOutcome(httpFixture{dir: dir, group: group}, phase); err != nil {
				return err
			}
			boundary()
			return errors.New("unreachable")
		}
	case "before-apply":
		h.BeforeApply = func(wal.Record) { boundary() }
	case "after-apply":
		h.AfterApply = func(wal.Record) { boundary() }
	case "snapshot-before-capture":
		h.BeforeCapture = boundary
	case "snapshot-after-capture":
		h.AfterCapture = func(snapshot.Document) { boundary() }
	case "snapshot-before-publish", "snapshot-after-publish":
		h.Publish = func(dir string, doc snapshot.Document) error {
			if phase == "snapshot-after-publish" {
				if err := snapshot.Publish(dir, doc); err != nil {
					return err
				}
			}
			boundary()
			return errors.New("unreachable")
		}
	case "before-response", "partial-response":
	default:
		t.Fatalf("unknown child phase %q", phase)
	}
	r := httpOpen(t, dir, httpOptions(), h)
	var backend api.Backend = r
	if phase == "before-response" {
		backend = beforeHTTPResponse{r, boundary}
	}
	l, err := api.ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if phase == "partial-response" {
		l = partialHTTPListener{l, boundary}
	}
	e := httpServe(t, backend, l)
	if err := json.NewEncoder(os.Stdout).Encode(struct{ Base, Run string }{e.base, r.RunID()}); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(phase, "snapshot-") {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || scanner.Text() != "snapshot" {
			t.Fatal("missing snapshot trigger")
		}
		_, err := r.Snapshot(context.Background())
		t.Fatalf("snapshot boundary returned: %v", err)
	}
	select {}
}

// Each child retains normal OS/drive caches. Valid unacknowledged frames may
// replay here; none of these cases establishes power-failure durability.
func TestNativeHTTPProcessBoundaryRecovery(t *testing.T) {
	if dir := os.Getenv("EXCHANGE_HTTP_FAULT_DIR"); dir != "" {
		runHTTPBoundaryChild(t, dir, os.Getenv("EXCHANGE_HTTP_FAULT_PHASE"))
		return
	}
	for _, phase := range []string{"before-write", "partial-write", "before-sync", "after-sync", "before-apply", "after-apply", "before-response", "partial-response", "snapshot-before-capture", "snapshot-after-capture", "snapshot-before-publish", "snapshot-after-publish"} {
		t.Run(phase, func(t *testing.T) {
			f := newHTTPFixture(t)
			groupPath := filepath.Join(t.TempDir(), "group.bin")
			if err := os.WriteFile(groupPath, f.group, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeHTTPProcessBoundaryRecovery$")
			cmd.Env = append(os.Environ(), "EXCHANGE_HTTP_FAULT_DIR="+f.dir, "EXCHANGE_HTTP_FAULT_PHASE="+phase, "EXCHANGE_HTTP_FAULT_GROUP="+groupPath)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stderr, err := os.CreateTemp(t.TempDir(), "child-stderr-")
			if err != nil {
				t.Fatal(err)
			}
			defer stderr.Close()
			cmd.Stderr = stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			lines, done := make(chan string, 32), make(chan error, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					lines <- scanner.Text()
				}
				close(lines)
				done <- errors.Join(scanner.Err(), cmd.Wait())
			}()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = httpAwait(t, done)
				}
			}()
			readyLine := httpAwait(t, lines)
			ready := httpDecode[struct{ Base, Run string }](t, []byte(readyLine))
			if ready.Run != f.run || !strings.HasPrefix(ready.Base, "http://127.0.0.1:") {
				t.Fatalf("bad ready report: %s", readyLine)
			}
			client := &http.Client{Transport: &http.Transport{Proxy: nil}}
			defer client.CloseIdleConnections()
			out := httpAsync(client, ready.Base, f.run, "alice", "POST", "/v1/orders", httpOrder("target", "BUY", 105, 2))
			isSnapshot := strings.HasPrefix(phase, "snapshot-")
			if isSnapshot {
				if got := httpStatus(t, httpAwait(t, out), 200); !bytes.Equal(got, f.target) {
					t.Fatal("acknowledged result changed")
				}
				if _, err := fmt.Fprintln(stdin, "snapshot"); err != nil {
					t.Fatal(err)
				}
			}
			if line := httpAwait(t, lines); line != "BOUNDARY "+phase {
				t.Fatalf("child did not reach selected boundary: %q", line)
			}
			if !isSnapshot {
				select {
				case got := <-out:
					t.Fatalf("request completed before kill: %+v", got)
				default:
				}
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = httpAwait(t, done)
			waited = true
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				data, _ := os.ReadFile(stderr.Name())
				t.Fatalf("expected owned-child SIGKILL: %v %s", err, data)
			}
			if !isSnapshot {
				got := httpAwait(t, out)
				if got.err == nil {
					t.Fatalf("interrupted request became a complete response: %d %s", got.status, got.body)
				}
				if phase == "partial-response" && (got.status != 200 || len(got.body) == 0 || len(got.body) >= len(f.target)) {
					t.Fatalf("did not receive a strict body prefix: %+v", got)
				}
			}
			seq, snapshotSeq := d.Int(2), d.Int(1)
			if phase == "before-write" || phase == "partial-write" {
				seq = 1
			}
			if phase == "snapshot-after-publish" {
				snapshotSeq = 2
			}
			assertHTTPRecoveredFrom(t, f, seq, snapshotSeq)
		})
	}
}
