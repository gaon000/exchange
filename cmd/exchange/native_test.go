//go:build darwin && cgo

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/wal"
)

func childCommand(args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "EXCHANGE_TEST_CLI_CHILD=1")
	return cmd
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *safeBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type process struct {
	cmd    *exec.Cmd
	lines  <-chan string
	done   <-chan error
	stderr *safeBuffer
	waited bool
}

func startProcess(t *testing.T, args ...string) *process {
	t.Helper()
	cmd := childCommand(args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &safeBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines, done := make(chan string, 8), make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
		done <- errors.Join(scanner.Err(), cmd.Wait())
	}()
	p := &process{cmd: cmd, lines: lines, done: done, stderr: stderr}
	t.Cleanup(func() {
		if !p.waited {
			_ = cmd.Process.Kill()
			_ = receive(t, done)
			p.waited = true
		}
	})
	return p
}
func (p *process) wait(t *testing.T) error {
	t.Helper()
	err := receive(t, p.done)
	p.waited = true
	return err
}
func (p *process) ready(t *testing.T) readyReport {
	t.Helper()
	line := receive(t, p.lines)
	var r readyReport
	if err := json.Unmarshal([]byte(line), &r); err != nil || r.Event != "ready" {
		t.Fatalf("ready report %q: %v; stderr %s", line, err, p.stderr.String())
	}
	return r
}

func initProcess(t *testing.T) (string, initializedReport) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	output, err := childCommand("init", "--config", configFixture(t), "--data-dir", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("init: %v: %s", err, output)
	}
	var report initializedReport
	if err := json.Unmarshal(output, &report); err != nil || report.Event != "initialized" || !d.ValidRunID(report.RunID) || report.DataDir != dir || report.Accounts != 2 || report.MaxCommands != d.DefaultMaxCommands {
		t.Fatalf("init report: %s: %v", output, err)
	}
	return dir, report
}

func clientFor(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

type response struct {
	status int
	body   []byte
	err    error
}

func send(client *http.Client, report readyReport, account, method, path, body string) response {
	r, err := http.NewRequest(method, "http://"+report.Listen+path, strings.NewReader(body))
	if err != nil {
		return response{err: err}
	}
	r.Header.Set("X-Run-ID", report.RunID)
	r.Header.Set("X-Account-ID", account)
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(r)
	if err != nil {
		return response{err: err}
	}
	data, err := io.ReadAll(res.Body)
	err = errors.Join(err, res.Body.Close())
	return response{res.StatusCode, data, err}
}
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return value
}

const orderJSON = `{"clientOrderId":"one","symbol":"BTC-USDT","side":"BUY","type":"LIMIT","timeInForce":"GTC","priceTicks":"100","quantityLots":"2"}`

func TestNativeCLIInitPreservesExistingAndIncompleteTargets(t *testing.T) {
	dir, report := initProcess(t)
	before, err := os.ReadFile(filepath.Join(dir, "genesis.bin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, existing := range []string{dir, t.TempDir()} {
		output, err := childCommand("init", "--config", configFixture(t), "--data-dir", existing).CombinedOutput()
		if err == nil || bytes.Contains(output, []byte(`"event":"initialized"`)) {
			t.Fatalf("existing target accepted: %s", output)
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, "genesis.bin"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("existing Genesis changed", err)
	}
	store, err := wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.Genesis().RunID != report.RunID {
		t.Fatal("identity replaced")
	}
	_ = store.Close()
	missingParent := filepath.Join(t.TempDir(), "missing", "run")
	output, err := childCommand("init", "--config", configFixture(t), "--data-dir", missingParent).CombinedOutput()
	if err == nil {
		t.Fatal("missing parent created", string(output))
	}
	if _, err := os.Stat(filepath.Dir(missingParent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("parent unexpectedly created", err)
	}
	// A reporting error happens after durable initialization. Preserve that run.
	newDir := filepath.Join(t.TempDir(), "run")
	err = run(context.Background(), []string{"init", "--config", configFixture(t), "--data-dir", newDir}, brokenWriter{}, io.Discard)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	store, err = wal.Open(newDir)
	if err != nil {
		t.Fatal("report failure lost initialized run", err)
	}
	_ = store.Close()
}

func TestNativeCLIServeValidatesBeforeRecoveryAndReleasesResources(t *testing.T) {
	dir, _ := initProcess(t)
	walPath := filepath.Join(dir, "commands.wal")
	log, err := os.OpenFile(walPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = log.Write([]byte("EXGRP001"))
	if err = errors.Join(err, log.Close()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatal(err)
	}
	l, err := api.ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for _, extra := range [][]string{{"--listen", "0.0.0.0:8080"}, {"--listen", l.Addr().String()}, {"--result-timeout", "0"}, {"--batch-commands", "0"}, {"--config", configFixture(t)}} {
		args := append([]string{"serve", "--data-dir", dir}, extra...)
		output, err := childCommand(args...).CombinedOutput()
		if err == nil || bytes.Contains(output, []byte(`"event":"ready"`)) {
			t.Fatalf("invalid serve succeeded: %s", output)
		}
		after, err := os.ReadFile(walPath)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("invalid serve performed recovery", err)
		}
	}
	// Failed recovery releases a reserved socket and never initializes a run.
	probe, err := api.ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	_ = probe.Close()
	for _, missing := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		output, err := childCommand("serve", "--data-dir", missing, "--listen", address).CombinedOutput()
		if err == nil || bytes.Contains(output, []byte(`"event":"ready"`)) {
			t.Fatalf("serve initialized target: %s", output)
		}
		probe, err = api.ListenLoopback(address)
		if err != nil {
			t.Fatal("failed start leaked socket", err)
		}
		_ = probe.Close()
		if entries, err := os.ReadDir(missing); err == nil && len(entries) != 0 {
			t.Fatal("serve created files")
		}
	}
	// Output failure after Open still closes runtime ownership and the socket.
	err = run(context.Background(), []string{"serve", "--data-dir", dir, "--listen", address}, brokenWriter{}, io.Discard)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	store, err := wal.Open(dir)
	if err != nil {
		t.Fatal("report failure leaked run lock", err)
	}
	_ = store.Close()
	probe, err = api.ListenLoopback(address)
	if err != nil {
		t.Fatal("report failure leaked socket", err)
	}
	_ = probe.Close()
}

func TestNativeCLISignalDrainsAcceptedCommandAndRestartsWithoutReseeding(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			dir, initialized := initProcess(t)
			p := startProcess(t, "serve", "--data-dir", dir, "--listen", "127.0.0.1:0", "--batch-delay", "1h", "--batch-commands", "2", "--request-limit", "32", "--pending-waiters", "4")
			report := p.ready(t)
			if report.RunID != initialized.RunID || report.Runtime.BatchDelay != "1h0m0s" || report.Runtime.RequestLimit != 32 || report.HTTP.RequestLimit != 32 || report.Recovery.ReplayedCommands != 0 || report.Recovery.Skipped == nil {
				t.Fatal(report)
			}
			if store, err := wal.Open(dir); err == nil || !durable.Contended(err) {
				if store != nil {
					_ = store.Close()
				}
				t.Fatal("CLI lifetime lock", err)
			}
			client := clientFor(t)
			if v := send(client, report, "alice", "GET", "/health/ready", ""); v.err != nil || v.status != 200 {
				t.Fatal(v)
			}
			order := make(chan response, 1)
			go func() { order <- send(client, report, "alice", "POST", "/v1/orders", orderJSON) }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				v := send(client, report, "alice", "GET", "/v1/results/place/one", "")
				if v.err != nil {
					t.Fatal(v.err)
				}
				if v.status == 202 {
					break
				}
				if v.status != 404 || time.Now().After(deadline) {
					t.Fatal("command never pending", v.status, string(v.body))
				}
			}
			if err := p.cmd.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			first := receive(t, order)
			if first.err != nil || first.status != 200 || decode[d.Result](t, first.body).CommandSeq != 1 {
				t.Fatal("signal dropped accepted command", first.status, string(first.body), first.err)
			}
			if err := p.wait(t); err != nil {
				t.Fatal(err, p.stderr.String())
			}
			// The hour-long batch did not block drain. The same port and run lock
			// can now be acquired by a new CLI process.
			p = startProcess(t, "serve", "--data-dir", dir, "--listen", report.Listen)
			restarted := p.ready(t)
			if restarted.RunID != report.RunID || restarted.Recovery.ReplayedCommands != 1 {
				t.Fatal(restarted)
			}
			retry := send(client, restarted, "alice", "POST", "/v1/orders", orderJSON)
			if retry.err != nil || retry.status != 200 || !bytes.Equal(first.body, retry.body) {
				t.Fatal("restart changed first result", retry.err, string(retry.body))
			}
			balance := send(client, restarted, "alice", "GET", "/v1/balances", "")
			v := decode[api.BalancesView](t, balance.body)
			if balance.err != nil || balance.status != 200 || v.AsOfSeq != 1 || v.USDT.AvailableAtoms != 99800 || v.USDT.ReservedAtoms != 200 {
				t.Fatal("restart reseeded", v, balance.err)
			}
			if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			if err := p.wait(t); err != nil {
				t.Fatal(err, p.stderr.String())
			}
		})
	}
}

func TestNativeCLIProcessKillRecoversAcknowledgedResult(t *testing.T) {
	dir, _ := initProcess(t)
	p := startProcess(t, "serve", "--data-dir", dir, "--listen", "127.0.0.1:0")
	report := p.ready(t)
	client := clientFor(t)
	first := send(client, report, "alice", "POST", "/v1/orders", orderJSON)
	if first.err != nil || first.status != 200 {
		t.Fatal(first.err, string(first.body))
	}
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(t); err == nil {
		t.Fatal("child was not killed")
	}
	p = startProcess(t, "serve", "--data-dir", dir, "--listen", report.Listen)
	restarted := p.ready(t)
	retry := send(client, restarted, "alice", "POST", "/v1/orders", orderJSON)
	if retry.err != nil || retry.status != 200 || !bytes.Equal(first.body, retry.body) {
		t.Fatal("process kill lost result", retry.err, string(retry.body))
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := p.wait(t); err != nil {
		t.Fatal(err, p.stderr.String())
	}
	// Process kill leaves OS caches running; this is not a power-loss test.
}

func TestNativeCLIRejectsSecondOwnerAndBadStoredData(t *testing.T) {
	dir, _ := initProcess(t)
	p := startProcess(t, "serve", "--data-dir", dir, "--listen", "127.0.0.1:0")
	_ = p.ready(t)
	output, err := childCommand("serve", "--data-dir", dir, "--listen", "127.0.0.1:0").CombinedOutput()
	if err == nil || bytes.Contains(output, []byte(`"event":"ready"`)) {
		t.Fatalf("second owner served: %s", output)
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	if err := p.wait(t); err != nil {
		t.Fatal(err)
	}
	genesisPath := filepath.Join(dir, "genesis.bin")
	original, err := os.ReadFile(genesisPath)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte{}, original...)
	corrupt[len(corrupt)-1] ^= 1
	if err := os.WriteFile(genesisPath, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	output, err = childCommand("serve", "--data-dir", dir, "--listen", "127.0.0.1:0").CombinedOutput()
	if err == nil || bytes.Contains(output, []byte(`"event":"ready"`)) {
		t.Fatalf("corrupt run served: %s", output)
	}
	after, err := os.ReadFile(genesisPath)
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatal("corrupt run reset", err)
	}
	// Restoring this disposable fixture lets us prove failed recovery released
	// its lock; production commands never repair corrupt data automatically.
	if err := os.WriteFile(genesisPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := rt.Open(dir, rt.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
