//go:build darwin && cgo

package runtime_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gaon000/exchange/internal/api"
	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/snapshot"
	"github.com/gaon000/exchange/internal/strictjson"
	"github.com/gaon000/exchange/internal/wal"
)

type crashMessage struct {
	Phase       string       `json:"phase"`
	Base        string       `json:"base,omitempty"`
	Run         string       `json:"run,omitempty"`
	SnapshotSeq d.Int        `json:"snapshotSeq,omitempty"`
	Records     []wal.Record `json:"records,omitempty"`
}

// This gate orders only distinct WAL keys on recorded replay. Same-ID copies
// can still wait in Submit concurrently; it does not imitate OS scheduling.
type crashAdmission struct {
	mu      sync.Mutex
	order   []d.Key
	next    int
	changed chan struct{}
}

func crashNewAdmission(inputs []d.CheckpointCommand, run string) *crashAdmission {
	a := &crashAdmission{changed: make(chan struct{})}
	for _, input := range inputs {
		c, _ := input.Command(run)
		a.order = append(a.order, c.Key())
	}
	return a
}
func (a *crashAdmission) enter(ctx context.Context, k d.Key) error {
	if a == nil {
		return nil
	}
	for {
		a.mu.Lock()
		pos := -1
		for i, key := range a.order {
			if k == key {
				pos = i
				break
			}
		}
		allowed := pos >= 0 && pos <= a.next
		ch := a.changed
		a.mu.Unlock()
		if allowed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
func (a *crashAdmission) complete(k d.Key) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.next < len(a.order) && a.order[a.next] == k {
		a.next++
		close(a.changed)
		a.changed = make(chan struct{})
	}
}

type crashBackend struct {
	*rt.Runtime
	commands      map[d.Key]d.Command
	order         *crashAdmission
	emit          func(crashMessage)
	mu            sync.Mutex
	entered, held int
	start         chan struct{}
}

func (b *crashBackend) Submit(ctx context.Context, c d.Command) (d.Result, error) {
	if expected, ok := b.commands[c.Key()]; !ok || expected != c {
		return b.Runtime.Submit(ctx, c)
	}
	b.mu.Lock()
	b.entered++
	if b.entered == 4 {
		b.emit(crashMessage{Phase: "wave-entered"})
		close(b.start)
	}
	b.mu.Unlock()
	select {
	case <-ctx.Done():
		return d.Result{}, ctx.Err()
	case <-b.start:
	}
	if err := b.order.enter(ctx, c.Key()); err != nil {
		return d.Result{}, err
	}
	result, err := b.Runtime.Submit(ctx, c)
	if err != nil {
		return result, err
	}
	b.order.complete(c.Key())
	b.mu.Lock()
	b.held++
	if b.held == 4 {
		b.emit(crashMessage{Phase: "wave-complete"})
	}
	b.mu.Unlock()
	// Never return a complete result for this wave. Only the parent-owned SIGKILL
	// ends it. A command may already be durable/applied while the client is UNKNOWN.
	select {}
}
func crashChild(t *testing.T) {
	b, err := crashRead(os.Getenv("EXCHANGE_CONCURRENT_CRASH_CASE"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var c crashCase
	if err := strictjson.Decode(b, &c); err != nil {
		t.Fatal(err)
	}
	var outputMu sync.Mutex
	emit := func(m crashMessage) {
		outputMu.Lock()
		defer outputMu.Unlock()
		if err := json.NewEncoder(os.Stdout).Encode(m); err != nil {
			panic(err)
		}
	}
	h := rt.HTTPTestHooks{}
	if c.Boundary == "wal-after-sync" {
		h.Append = func(s *wal.Store, records []wal.Record) error {
			if err := s.Append(records); err != nil {
				return err
			}
			emit(crashMessage{Phase: "boundary", Records: records})
			select {}
		}
	} else {
		h.Publish = func(dir string, doc snapshot.Document) error {
			if c.Boundary == "snapshot-after-publish" {
				if err := snapshot.Publish(dir, doc); err != nil {
					return err
				}
			}
			emit(crashMessage{Phase: "boundary", SnapshotSeq: doc.SnapshotSeq})
			select {}
		}
	}
	r, err := rt.OpenHTTPTestRuntime(os.Getenv("EXCHANGE_CONCURRENT_CRASH_RUN"), httpOptions(), h)
	if err != nil {
		t.Fatal(err)
	}
	backend := &crashBackend{Runtime: r, commands: map[d.Key]d.Command{}, emit: emit, start: make(chan struct{})}
	for _, input := range c.Attempts {
		cmd, err := input.Command(r.RunID())
		if err != nil {
			t.Fatal(err)
		}
		backend.commands[cmd.Key()] = cmd
	}
	if c.Recorded != nil {
		backend.order = crashNewAdmission(c.Recorded.BeforeKill, r.RunID())
	}
	opts := api.DefaultOptions()
	opts.ResultTimeout = 60 * time.Second
	server, err := api.New(backend, opts)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := api.ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := server.Serve(listener); err != nil {
			panic(err)
		}
	}()
	emit(crashMessage{Phase: "ready", Base: "http://" + listener.Addr().String(), Run: r.RunID()})
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || scanner.Text() != "snapshot" {
		t.Fatal("expected snapshot trigger or parent SIGKILL")
	}
	if c.Boundary == "wal-after-sync" {
		t.Fatal("unexpected snapshot in WAL case")
	}
	_, err = r.Snapshot(context.Background())
	t.Fatalf("snapshot boundary returned: %v", err)
}

type crashProcess struct {
	cmd                *exec.Cmd
	input              io.WriteCloser
	messages           chan crashMessage
	readDone, waitDone chan struct{}
	waitErr, readErr   error
	stdout, stderr     *os.File
}

func startCrashProcess(ctx context.Context, c crashCase, dir string) (_ *crashProcess, err error) {
	casePath := filepath.Join(dir, "case.json")
	if err := crashJSON(casePath, c); err != nil {
		return nil, err
	}
	p := &crashProcess{messages: make(chan crashMessage, 16), readDone: make(chan struct{}), waitDone: make(chan struct{})}
	defer func() {
		if err != nil {
			if p.stdout != nil {
				_ = p.stdout.Close()
			}
			if p.stderr != nil {
				_ = p.stderr.Close()
			}
		}
	}()
	p.stdout, err = os.OpenFile(filepath.Join(dir, "child.stdout.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	p.stderr, err = os.OpenFile(filepath.Join(dir, "child.stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	p.cmd = exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConcurrentCrashCommand$", "-test.timeout=45s")
	env := []string{}
	for _, value := range os.Environ() {
		if !bytes.HasPrefix([]byte(value), []byte("EXCHANGE_CONCURRENT_CRASH_")) {
			env = append(env, value)
		}
	}
	p.cmd.Env = append(env, "EXCHANGE_CONCURRENT_CRASH_MODE=child", "EXCHANGE_CONCURRENT_CRASH_CASE="+casePath, "EXCHANGE_CONCURRENT_CRASH_RUN="+filepath.Join(dir, "run"))
	p.cmd.Stderr = p.stderr
	pipe, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	p.cmd.Stdout = writer
	p.input, err = p.cmd.StdinPipe()
	if err != nil {
		_ = pipe.Close()
		_ = writer.Close()
		return nil, err
	}
	if err := p.cmd.Start(); err != nil {
		_ = p.input.Close()
		_ = pipe.Close()
		_ = writer.Close()
		return nil, err
	}
	_ = writer.Close()
	go func() {
		defer close(p.readDone)
		defer close(p.messages)
		defer pipe.Close()
		scanner := bufio.NewScanner(io.TeeReader(pipe, p.stdout))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			var m crashMessage
			if err := strictjson.Decode(scanner.Bytes(), &m); err != nil {
				p.readErr = err
				return
			}
			select {
			case p.messages <- m:
			case <-ctx.Done():
				return
			}
		}
		p.readErr = scanner.Err()
	}()
	go func() { p.waitErr = p.cmd.Wait(); close(p.waitDone) }()
	return p, nil
}
func (p *crashProcess) phase(ctx context.Context, want string) (crashMessage, error) {
	select {
	case m, ok := <-p.messages:
		if !ok {
			return m, errors.New("child stdout closed before " + want)
		}
		if m.Phase != want {
			return m, fmt.Errorf("child phase want %s got %s", want, m.Phase)
		}
		return m, nil
	case <-ctx.Done():
		return crashMessage{}, ctx.Err()
	}
}
func (p *crashProcess) kill(ctx context.Context) error {
	if err := p.cmd.Process.Kill(); err != nil {
		return err
	}
	select {
	case <-p.waitDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	var exit *exec.ExitError
	if !errors.As(p.waitErr, &exit) {
		return fmt.Errorf("missing child kill status: %v", p.waitErr)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		return fmt.Errorf("expected SIGKILL, got %v", p.waitErr)
	}
	return nil
}
func (p *crashProcess) close() error {
	if p == nil {
		return nil
	}
	select {
	case <-p.waitDone:
	default:
		_ = p.cmd.Process.Kill()
		<-p.waitDone
	}
	_ = p.input.Close()
	<-p.readDone
	return errors.Join(p.readErr, p.stdout.Sync(), p.stdout.Close(), p.stderr.Sync(), p.stderr.Close())
}

type crashObservation struct {
	Client    int                 `json:"client"`
	Command   d.CheckpointCommand `json:"command"`
	Outcome   string              `json:"outcome"`
	Status    int                 `json:"status"`
	Response  string              `json:"response,omitempty"`
	Error     string              `json:"error,omitempty"`
	ElapsedNS int64               `json:"elapsedNs"`
}

func crashRequest(ctx context.Context, client *http.Client, base, run, account, method, path string, payload any) httpReply {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return httpReply{err: err}
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(body))
	if err != nil {
		return httpReply{err: err}
	}
	req.Header.Set("X-Run-ID", run)
	req.Header.Set("X-Account-ID", account)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return httpReply{err: err}
	}
	b, readErr := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	err = errors.Join(readErr, res.Body.Close())
	if len(b) > 1<<20 {
		err = errors.Join(err, errors.New("response too large"))
	}
	return httpReply{status: res.StatusCode, body: b, err: err}
}
func crashPost(ctx context.Context, client *http.Client, base, run string, input d.CheckpointCommand) httpReply {
	path, payload := "/v1/orders", any(input.Place)
	if input.Kind == d.CancelOrder {
		path, payload = "/v1/cancellations", input.Cancel
	}
	return crashRequest(ctx, client, base, run, input.AccountID, "POST", path, payload)
}
func crashWant(reply httpReply, status int, want any) error {
	if reply.err != nil || reply.status != status {
		return fmt.Errorf("HTTP want %d got %d: %s: %w", status, reply.status, reply.body, reply.err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if !bytes.Equal(b, reply.body) {
		return fmt.Errorf("HTTP result mismatch: want %s got %s", b, reply.body)
	}
	return nil
}
func crashInput(c d.Command) d.CheckpointCommand {
	input := d.CheckpointCommand{AccountID: c.AccountID, Kind: c.Kind}
	if c.Kind == d.PlaceOrder {
		v := c.Place
		input.Place = &v
	} else {
		v := c.Cancel
		input.Cancel = &v
	}
	return input
}
func crashJournal(dir string) (g wal.Genesis, records []wal.Record, err error) {
	s, err := wal.Open(dir)
	if err != nil {
		return g, nil, err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	g = s.Genesis()
	err = s.Replay(func(record wal.Record) error { records = append(records, record); return nil })
	return
}
func crashModel(g wal.Genesis, records []wal.Record, allowed map[d.Key]d.Command) (*engine.Engine, error) {
	model, err := engine.New(g.RunID, g.Config)
	if err != nil {
		return nil, err
	}
	seen := map[d.Key]bool{}
	for i, record := range records {
		c := record.Command
		if record.Seq != d.Int(i+1) || seen[c.Key()] || allowed[c.Key()] != c {
			return nil, errors.New("unexpected WAL input, sequence or duplicate key")
		}
		seen[c.Key()] = true
		if _, err := model.Apply(record.Seq, c); err != nil {
			return nil, err
		}
	}
	return model, model.CheckInvariants()
}
func crashFirst(model *engine.Engine) map[d.Key]d.Result {
	first := map[d.Key]d.Result{}
	for _, r := range model.State().Results {
		first[r.Command.Key()] = r.Result
	}
	return first
}

type crashSession struct {
	r      *rt.Runtime
	api    *api.Server
	client *http.Client
	base   string
	done   chan error
}

func crashOpenSession(dir string) (_ *crashSession, err error) {
	r, err := rt.Open(dir, httpOptions())
	if err != nil {
		return nil, err
	}
	s := &crashSession{r: r, done: make(chan error, 1)}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.close())
		}
	}()
	s.api, err = api.New(r, api.DefaultOptions())
	if err != nil {
		return nil, err
	}
	listener, err := api.ListenLoopback("127.0.0.1:0")
	if err != nil {
		s.api = nil
		return nil, err
	}
	s.base = "http://" + listener.Addr().String()
	s.client = &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	go func() { s.done <- s.api.Serve(listener) }()
	return s, nil
}
func (s *crashSession) close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.api != nil {
		err = s.api.Close()
		select {
		case e := <-s.done:
			if !errors.Is(e, http.ErrServerClosed) {
				err = errors.Join(err, e)
			}
		case <-time.After(5 * time.Second):
			err = errors.Join(err, errors.New("HTTP close deadline"))
		}
		s.api = nil
	}
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	if s.r != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = errors.Join(err, s.r.Shutdown(ctx))
		cancel()
		s.r = nil
	}
	return err
}
func crashCapture(ctx context.Context, s *crashSession, dir string, g wal.Genesis) (d.Checkpoint, error) {
	saved, err := s.r.Snapshot(ctx)
	if err != nil {
		return d.Checkpoint{}, err
	}
	candidates, err := snapshot.List(dir)
	if err != nil || len(candidates) == 0 {
		return d.Checkpoint{}, errors.Join(err, errors.New("missing published checkpoint"))
	}
	doc, err := snapshot.Load(dir, candidates[0], snapshot.Identity{RunID: g.RunID, ConfigHash: g.ConfigHash, RulesVersion: g.Config.RulesVersion, ResponseVersion: g.Config.ResponseVersion}, saved.SnapshotSeq)
	if err != nil {
		return d.Checkpoint{}, err
	}
	if doc.SnapshotSeq != saved.SnapshotSeq {
		return d.Checkpoint{}, errors.New("capture selected wrong snapshot")
	}
	return doc.State, nil
}
func crashViews(ctx context.Context, s *crashSession, model *engine.Engine, dir, label string) (err error) {
	state := model.State()
	run := s.r.RunID()
	trace := []map[string]any{}
	defer func() { err = errors.Join(err, crashJSON(filepath.Join(dir, label+"-http.json"), trace)) }()
	get := func(account, path string, want any) error {
		reply := crashRequest(ctx, s.client, s.base, run, account, "GET", path, nil)
		trace = append(trace, map[string]any{"account": account, "path": path, "status": reply.status, "response": string(reply.body)})
		return crashWant(reply, 200, want)
	}
	for _, a := range state.Accounts {
		want := api.BalancesView{RunID: run, AsOfSeq: state.AppliedSeq, BTC: api.BaseBalance{AvailableLots: a.Balances.Base.Available, ReservedLots: a.Balances.Base.Reserved}, USDT: api.QuoteBalance{AvailableAtoms: a.Balances.Quote.Available, ReservedAtoms: a.Balances.Quote.Reserved}}
		if err := get(a.AccountID, "/v1/balances", want); err != nil {
			return err
		}
	}
	for _, o := range state.Orders {
		if err := get(o.Ref.AccountID, "/v1/orders/"+o.Ref.ClientOrderID, api.OrderView{RunID: run, AsOfSeq: state.AppliedSeq, Order: &o.Order}); err != nil {
			return err
		}
	}
	bids, asks, err := model.Book(100)
	if err != nil {
		return err
	}
	book := api.BookView{RunID: run, AsOfSeq: state.AppliedSeq, Bids: []api.Level{}, Asks: []api.Level{}}
	for _, v := range bids {
		book.Bids = append(book.Bids, api.Level{PriceTicks: v.PriceTicks, QuantityLots: v.QuantityLots})
	}
	for _, v := range asks {
		book.Asks = append(book.Asks, api.Level{PriceTicks: v.PriceTicks, QuantityLots: v.QuantityLots})
	}
	if err := get("alice", "/v1/book?limit=100", book); err != nil {
		return err
	}
	trades := api.TradesView{RunID: run, AsOfSeq: state.AppliedSeq, ThroughSeq: state.AppliedSeq, Trades: []api.Trade{}}
	for _, v := range state.Trades {
		trades.Trades = append(trades.Trades, api.Trade{TradeID: v.TradeID, PriceTicks: v.PriceTicks, QuantityLots: v.QuantityLots, TakerSide: v.TakerSide})
	}
	if err := get("alice", "/v1/trades?limit=1000", trades); err != nil {
		return err
	}
	for _, result := range state.Results {
		c := result.Command
		kind := "place"
		if c.Kind == d.CancelOrder {
			kind = "cancel"
		}
		if err := get(c.AccountID, "/v1/results/"+kind+"/"+c.Key().RequestID, result.Result); err != nil {
			return err
		}
	}
	return nil
}
func crashPrefix(prior, current []wal.Record) error {
	if len(current) < len(prior) || !reflect.DeepEqual(prior, current[:len(prior)]) {
		return errors.New("previous WAL prefix changed")
	}
	return nil
}
func crashKeys(c crashCase, run string) map[d.Key]d.Command {
	allowed := map[d.Key]d.Command{}
	inputs := append(append([]d.CheckpointCommand{}, c.Setup...), c.Attempts...)
	if c.Tail != nil {
		inputs = append(inputs, *c.Tail)
	}
	for _, input := range inputs {
		cmd, _ := input.Command(run)
		allowed[cmd.Key()] = cmd
	}
	return allowed
}
func crashRunCase(parent context.Context, c crashCase, dir string, saveOrder func(crashOrder) error) (summary map[string]any, err error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	var child *crashProcess
	var s *crashSession
	var wg sync.WaitGroup
	observations := make([]crashObservation, 0)
	defer func() {
		cancel()
		err = errors.Join(err, child.close())
		wg.Wait()
		err = errors.Join(err, s.close(), crashJSON(filepath.Join(dir, "initial-observations.json"), observations))
	}()
	runDir := filepath.Join(dir, "run")
	store, err := wal.Init(runDir, crashConfig())
	if err != nil {
		return nil, err
	}
	g := store.Genesis()
	if err := store.Close(); err != nil {
		return nil, err
	}
	model, err := engine.New(g.RunID, g.Config)
	if err != nil {
		return nil, err
	}
	s, err = crashOpenSession(runDir)
	if err != nil {
		return nil, err
	}
	acknowledged := map[d.Key]httpReply{}
	ackEvidence := []map[string]any{}
	for _, input := range c.Setup {
		cmd, _ := input.Command(g.RunID)
		want, err := model.Apply(model.AppliedSeq()+1, cmd)
		if err != nil {
			return nil, err
		}
		reply := crashPost(ctx, s.client, s.base, g.RunID, input)
		if err := crashWant(reply, 200, want); err != nil {
			return nil, err
		}
		acknowledged[cmd.Key()] = reply
		ackEvidence = append(ackEvidence, map[string]any{"command": input, "status": reply.status, "response": string(reply.body)})
	}
	prefixState, err := crashCapture(ctx, s, runDir, g)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(prefixState, model.Capture()) {
		return nil, errors.New("setup checkpoint mismatch")
	}
	if err := s.close(); err != nil {
		return nil, err
	}
	_, prefix, err := crashJournal(runDir)
	if err != nil {
		return nil, err
	}
	child, err = startCrashProcess(ctx, c, dir)
	if err != nil {
		return nil, err
	}
	ready, err := child.phase(ctx, "ready")
	if err != nil {
		return nil, err
	}
	if ready.Run != g.RunID {
		return nil, errors.New("child run identity changed")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 20 * time.Second}
	defer client.CloseIdleConnections()
	observations = make([]crashObservation, len(c.Attempts))
	completed := make([]chan struct{}, len(c.Attempts))
	for i, input := range c.Attempts {
		completed[i] = make(chan struct{})
		wg.Add(1)
		go func(i int, input d.CheckpointCommand) {
			defer wg.Done()
			defer close(completed[i])
			started := time.Now()
			reply := crashPost(ctx, client, ready.Base, g.RunID, input)
			o := crashObservation{Client: i, Command: input, Outcome: "UNKNOWN", Status: reply.status, Response: string(reply.body), ElapsedNS: time.Since(started).Nanoseconds()}
			if reply.err != nil {
				o.Error = reply.err.Error()
			} else {
				o.Outcome = "HTTP_RESPONSE"
			}
			observations[i] = o
		}(i, input)
	}
	if _, err := child.phase(ctx, "wave-entered"); err != nil {
		return nil, err
	}
	if c.Tail != nil {
		if _, err := child.phase(ctx, "wave-complete"); err != nil {
			return nil, err
		}
		if _, err := fmt.Fprintln(child.input, "snapshot"); err != nil {
			return nil, err
		}
	}
	boundary, err := child.phase(ctx, "boundary")
	if err != nil {
		return nil, err
	}
	if c.Tail == nil {
		if len(boundary.Records) != 1 || boundary.Records[0].Seq != 3 {
			return nil, errors.New("WAL gate did not follow one real durable Append")
		}
	} else {
		if boundary.SnapshotSeq != 5 || len(boundary.Records) != 0 {
			return nil, errors.New("unexpected concurrent snapshot boundary")
		}
		tail := crashPost(ctx, client, ready.Base, g.RunID, *c.Tail)
		if tail.err != nil || tail.status != 200 {
			return nil, fmt.Errorf("tail was not acknowledged during snapshot wait: %+v", tail)
		}
		cmd, _ := c.Tail.Command(g.RunID)
		acknowledged[cmd.Key()] = tail
		ackEvidence = append(ackEvidence, map[string]any{"command": *c.Tail, "status": tail.status, "response": string(tail.body)})
	}
	if err := crashJSON(filepath.Join(dir, "acknowledged.json"), ackEvidence); err != nil {
		return nil, err
	}
	for _, done := range completed {
		select {
		case <-done:
			return nil, errors.New("initial call completed before owned SIGKILL")
		default:
		}
	}
	if err := crashJSON(filepath.Join(dir, "kill-boundary.json"), map[string]any{"boundary": c.Boundary, "pid": child.cmd.Process.Pid, "message": boundary, "initialCallsStillInFlight": 4, "signal": "SIGKILL", "powerLossTested": false}); err != nil {
		return nil, err
	}
	if err := child.kill(ctx); err != nil {
		return nil, err
	}
	wg.Wait()
	for _, o := range observations {
		if o.Status != 0 || o.Error == "" {
			return nil, errors.New("interrupted call did not retain transport UNKNOWN")
		}
	}
	if err := crashJSON(filepath.Join(dir, "kill-confirmed.json"), map[string]any{"signal": "SIGKILL", "waitStatusVerified": true}); err != nil {
		return nil, err
	}
	_, records, err := crashJournal(runDir)
	if err != nil {
		return nil, err
	}
	if err := crashJSON(filepath.Join(dir, "crash-wal.json"), records); err != nil {
		return nil, err
	}
	if err := crashPrefix(prefix, records); err != nil {
		return nil, err
	}
	if c.Tail == nil {
		if len(records) != 3 || records[2] != boundary.Records[0] {
			return nil, errors.New("durable Append did not replay")
		}
	} else if len(records) != 6 {
		return nil, errors.New("snapshot wave/tail missing or duplicated")
	}
	allowed := crashKeys(c, g.RunID)
	model, err = crashModel(g, records, allowed)
	if err != nil {
		return nil, err
	}
	first := crashFirst(model)
	for key, reply := range acknowledged {
		result, ok := first[key]
		if !ok {
			return nil, errors.New("acknowledged command missing from crash WAL")
		}
		if err := crashWant(reply, 200, result); err != nil {
			return nil, err
		}
	}
	order := crashOrder{}
	end := len(records)
	if c.Tail != nil {
		end--
	}
	for _, record := range records[len(prefix):end] {
		order.BeforeKill = append(order.BeforeKill, crashInput(record.Command))
	}
	if c.Recorded != nil && !reflect.DeepEqual(c.Recorded.BeforeKill, order.BeforeKill) {
		return nil, errors.New("recorded pre-kill order changed")
	}
	if c.Recorded != nil {
		order.AfterRetry = c.Recorded.AfterRetry
	}
	if err := saveOrder(order); err != nil {
		return nil, err
	}
	// Opening the journal above observes persisted inputs. Only Runtime.Open below
	// confirms recovery, restores a snapshot and opens readiness.
	beforeHash, err := crashHash(filepath.Join(runDir, "commands.wal"))
	if err != nil {
		return nil, err
	}
	s, err = crashOpenSession(runDir)
	if err != nil {
		return nil, err
	}
	report := s.r.Recovery()
	snapshotSeq := d.Int(2)
	if c.Boundary == "snapshot-after-publish" {
		snapshotSeq = 5
	}
	if report.SnapshotSeq != snapshotSeq || report.ReplayedCommands != model.AppliedSeq()-snapshotSeq || report.SnapshotFile == "" || len(report.Skipped) != 0 {
		return nil, fmt.Errorf("wrong selected snapshot/tail: %+v", report)
	}
	if err := crashJSON(filepath.Join(dir, "recovery.json"), report); err != nil {
		return nil, err
	}
	if err := crashViews(ctx, s, model, dir, "recovered"); err != nil {
		return nil, err
	}
	// Follow-up observation is separate from the interrupted client's UNKNOWN.
	followup := []map[string]any{}
	unknown := 0
	seen := map[d.Key]bool{}
	for _, input := range c.Attempts {
		cmd, _ := input.Command(g.RunID)
		if seen[cmd.Key()] {
			continue
		}
		seen[cmd.Key()] = true
		kind := "place"
		if cmd.Kind == d.CancelOrder {
			kind = "cancel"
		}
		reply := crashRequest(ctx, s.client, s.base, g.RunID, cmd.AccountID, "GET", "/v1/results/"+kind+"/"+cmd.Key().RequestID, nil)
		if want, ok := first[cmd.Key()]; ok {
			if err := crashWant(reply, 200, want); err != nil {
				return nil, err
			}
		} else {
			if err := crashWant(reply, 404, map[string]string{"state": "UNKNOWN"}); err != nil {
				return nil, err
			}
			unknown++
		}
		followup = append(followup, map[string]any{"command": input, "status": reply.status, "response": string(reply.body), "originalOutcome": "UNKNOWN"})
	}
	if err := crashJSON(filepath.Join(dir, "followup-results.json"), followup); err != nil {
		return nil, err
	}
	recovered, err := crashCapture(ctx, s, runDir, g)
	if err != nil {
		return nil, err
	}
	if err := crashJSON(filepath.Join(dir, "recovered-expected.json"), model.Capture()); err != nil {
		return nil, err
	}
	if err := crashJSON(filepath.Join(dir, "recovered-actual.json"), recovered); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(model.Capture(), recovered) {
		return nil, errors.New("recovered complete checkpoint/FIFO differs from actual WAL model")
	}
	afterHash, err := crashHash(filepath.Join(runDir, "commands.wal"))
	if err != nil || beforeHash != afterHash {
		return nil, errors.Join(err, errors.New("recovery/queries/snapshot changed WAL"))
	}
	retries := make([]httpReply, len(c.Attempts))
	var retryWG sync.WaitGroup
	var retryGate *crashAdmission
	if c.Recorded != nil && c.Recorded.AfterRetry != nil {
		retryGate = crashNewAdmission(*c.Recorded.AfterRetry, g.RunID)
	}
	start := make(chan struct{})
	for i, input := range c.Attempts {
		retryWG.Add(1)
		go func(i int, input d.CheckpointCommand) {
			defer retryWG.Done()
			<-start
			cmd, _ := input.Command(g.RunID)
			if _, stored := first[cmd.Key()]; !stored {
				if err := retryGate.enter(ctx, cmd.Key()); err != nil {
					retries[i] = httpReply{err: err}
					return
				}
			}
			retries[i] = crashPost(ctx, s.client, s.base, g.RunID, input)
			if retries[i].err == nil && retries[i].status == 200 {
				retryGate.complete(cmd.Key())
			}
		}(i, input)
	}
	close(start)
	retryWG.Wait()
	if err := crashJSON(filepath.Join(dir, "retry-responses.json"), func() []map[string]any {
		rows := []map[string]any{}
		for i, reply := range retries {
			row := map[string]any{"client": i, "command": c.Attempts[i], "status": reply.status, "response": string(reply.body), "originalOutcome": "UNKNOWN"}
			if reply.err != nil {
				row["error"] = reply.err.Error()
			}
			rows = append(rows, row)
		}
		return rows
	}()); err != nil {
		return nil, err
	}
	for _, reply := range retries {
		if reply.err != nil || reply.status != 200 {
			return nil, fmt.Errorf("retry failed: %+v", reply)
		}
	}
	finalState, err := crashCapture(ctx, s, runDir, g)
	if err != nil {
		return nil, err
	}
	if err := s.close(); err != nil {
		return nil, err
	}
	_, finalRecords, err := crashJournal(runDir)
	if err != nil {
		return nil, err
	}
	if err := crashJSON(filepath.Join(dir, "final-wal.json"), finalRecords); err != nil {
		return nil, err
	}
	if err := crashPrefix(records, finalRecords); err != nil {
		return nil, err
	}
	if len(finalRecords) != len(allowed) {
		return nil, errors.New("missing/duplicated logical command after retry")
	}
	finalModel, err := crashModel(g, finalRecords, allowed)
	if err != nil {
		return nil, err
	}
	finalFirst := crashFirst(finalModel)
	for k, prior := range first {
		if !reflect.DeepEqual(prior, finalFirst[k]) {
			return nil, errors.New("stored first response changed after retry")
		}
	}
	for i, reply := range retries {
		cmd, _ := c.Attempts[i].Command(g.RunID)
		if err := crashWant(reply, 200, finalFirst[cmd.Key()]); err != nil {
			return nil, err
		}
	}
	if !reflect.DeepEqual(finalState, finalModel.Capture()) {
		return nil, errors.New("post-retry checkpoint does not match actual stored order")
	}
	afterRetry := []d.CheckpointCommand{}
	for _, record := range finalRecords[len(records):] {
		afterRetry = append(afterRetry, crashInput(record.Command))
	}
	order.AfterRetry = &afterRetry
	if c.Recorded != nil && c.Recorded.AfterRetry != nil && !reflect.DeepEqual(*c.Recorded.AfterRetry, afterRetry) {
		return nil, errors.New("recorded retry order changed")
	}
	if err := saveOrder(order); err != nil {
		return nil, err
	}
	if err := crashJSON(filepath.Join(dir, "final-expected.json"), finalModel.Capture()); err != nil {
		return nil, err
	}
	if err := crashJSON(filepath.Join(dir, "final-actual.json"), finalState); err != nil {
		return nil, err
	}
	// A second open verifies initial responses before any further POST, then all
	// logical IDs are retried again. Neither trade counts nor WAL bytes may grow.
	hash, err := crashHash(filepath.Join(runDir, "commands.wal"))
	if err != nil {
		return nil, err
	}
	s, err = crashOpenSession(runDir)
	if err != nil {
		return nil, err
	}
	if err := crashViews(ctx, s, finalModel, dir, "second-recovery"); err != nil {
		return nil, err
	}
	for _, record := range finalRecords {
		reply := crashPost(ctx, s.client, s.base, g.RunID, crashInput(record.Command))
		if err := crashWant(reply, 200, finalFirst[record.Command.Key()]); err != nil {
			return nil, err
		}
	}
	if err := s.close(); err != nil {
		return nil, err
	}
	hashAfter, err := crashHash(filepath.Join(runDir, "commands.wal"))
	if err != nil || hash != hashAfter {
		return nil, errors.Join(err, errors.New("second restart/identical retries changed WAL"))
	}
	summary = map[string]any{"boundary": c.Boundary, "signal": "SIGKILL", "initialClients": 4, "originalUnknown": 4, "followup404Unknown": unknown, "crashCommands": len(records), "finalCommands": len(finalRecords), "snapshotSeq": report.SnapshotSeq, "tailCommands": report.ReplayedCommands, "trades": len(finalState.Trades), "concurrentRetries": 4, "secondRecoveryRetries": len(finalRecords), "powerLossTested": false}
	return summary, crashJSON(filepath.Join(dir, "summary.json"), summary)
}
func executeCrashSuite(ctx context.Context, p crashPlan, output string) error {
	b, _ := json.Marshal(p)
	var replay crashPlan
	if err := json.Unmarshal(b, &replay); err != nil {
		return err
	}
	reports := []map[string]any{}
	replayIndex := 0
	for i, c := range p.Cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir := filepath.Join(output, c.Boundary)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		report, err := crashRunCase(ctx, c, dir, func(order crashOrder) error {
			replay.Cases[i].Recorded = &order
			if err := validateCrashPlan(replay); err != nil {
				return err
			}
			path := filepath.Join(output, fmt.Sprintf("replay-%03d.json", replayIndex))
			replayIndex++
			return crashJSON(path, replay)
		})
		if err != nil {
			return fmt.Errorf("%s: %w", c.Boundary, err)
		}
		reports = append(reports, report)
	}
	return crashJSON(filepath.Join(output, "summary.json"), map[string]any{"status": "PASSED", "seed": p.Seed, "cases": reports, "processKillTested": true, "powerLossTested": false, "reproduce": crashReproduction(output)})
}

func crashTestParent(t *testing.T) string {
	t.Helper()
	if parent := os.Getenv("EXCHANGE_SCENARIO_EVIDENCE_PARENT"); parent != "" {
		dir, err := os.MkdirTemp(parent, "random-recovery-crash-")
		if err != nil {
			t.Fatal(err)
		}
		t.Log("retained crash evidence:", dir)
		return dir
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestNativeConcurrentCrashRecoveryAndRecordedReplay(t *testing.T) {
	for _, seed := range []int64{17, 42} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			parent := crashTestParent(t)
			p, _ := makeCrashPlan(seed)
			out := filepath.Join(parent, "original")
			if err := runCrashSuite(p, out, io.Discard); err != nil {
				t.Fatal(err)
			}
			b, err := crashRead(filepath.Join(out, "replay-005.json"), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			var replay crashPlan
			if err := strictjson.Decode(b, &replay); err != nil {
				t.Fatal(err)
			}
			fresh := filepath.Join(parent, "replay")
			if err := runCrashSuite(replay, fresh, io.Discard); err != nil {
				t.Fatal(err)
			}
			for _, boundary := range crashBoundaries {
				for _, name := range []string{"final-actual.json", "crash-wal.json", "final-wal.json"} {
					a, err := os.ReadFile(filepath.Join(out, boundary, name))
					if err != nil {
						t.Fatal(err)
					}
					b, err := os.ReadFile(filepath.Join(fresh, boundary, name))
					if err != nil {
						t.Fatal(err)
					}
					var cp d.Checkpoint
					data, err := os.ReadFile(filepath.Join(out, boundary, "final-actual.json"))
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(data, &cp); err != nil {
						t.Fatal(err)
					}
					var other d.Checkpoint
					data, err = os.ReadFile(filepath.Join(fresh, boundary, "final-actual.json"))
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(data, &other); err != nil {
						t.Fatal(err)
					}
					a = bytes.ReplaceAll(a, []byte(cp.Results[0].Response.RunID), []byte("RUN"))
					b = bytes.ReplaceAll(b, []byte(other.Results[0].Response.RunID), []byte("RUN"))
					if !bytes.Equal(a, b) {
						t.Fatal("recorded logical state/order changed", boundary, name)
					}
					if len(cp.Trades) < 1 || len(cp.Results) < 5 {
						t.Fatal("missing actual fills/first results")
					}
				}
			}
			before, _ := crashHash(filepath.Join(out, "replay-005.json"))
			if err := runCrashSuite(p, out, io.Discard); err == nil {
				t.Fatal("reused original output")
			}
			after, _ := crashHash(filepath.Join(out, "replay-005.json"))
			if before != after {
				t.Fatal("source changed")
			}
		})
	}
}

type crashFailedWriter struct{}

func (crashFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestNativeConcurrentCrashFailureKeepsReproduction(t *testing.T) {
	parent := crashTestParent(t)
	p, _ := makeCrashPlan(701)
	out := filepath.Join(parent, "failed")
	if err := runCrashSuite(p, out, crashFailedWriter{}); err == nil {
		t.Fatal("stdout failure accepted")
	}
	b, err := os.ReadFile(filepath.Join(out, "failed.json"))
	if err != nil || !bytes.Contains(b, []byte("replay-005.json")) {
		t.Fatal("missing failure reproduction", err, string(b))
	}
	if _, err := os.Lstat(filepath.Join(out, "completed.json")); !os.IsNotExist(err) {
		t.Fatal("failed run completed")
	}
	replay, _, _, err := parseCrashCommand([]string{"--replay", filepath.Join(out, "replay-005.json"), "--output", filepath.Join(parent, "reproduced")}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := runCrashSuite(replay, filepath.Join(parent, "reproduced"), io.Discard); err != nil {
		t.Fatal(err)
	}
}
func TestNativeConcurrentCrashCancellationReapsOwnedChild(t *testing.T) {
	parent := crashTestParent(t)
	dir := filepath.Join(parent, "cancelled")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := wal.Init(filepath.Join(dir, "run"), crashConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	p, _ := makeCrashPlan(17)
	ctx, cancel := context.WithCancel(context.Background())
	child, err := startCrashProcess(ctx, p.Cases[0], dir)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	readyCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	ready, err := child.phase(readyCtx, "ready")
	if err != nil {
		cancel()
		_ = child.close()
		t.Fatal(err)
	}
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	var requests sync.WaitGroup
	for _, input := range p.Cases[0].Attempts {
		requests.Add(1)
		go func(input d.CheckpointCommand) {
			defer requests.Done()
			_ = crashPost(ctx, client, ready.Base, ready.Run, input)
		}(input)
	}
	for _, phase := range []string{"wave-entered", "boundary"} {
		if _, err := child.phase(readyCtx, phase); err != nil {
			cancel()
			_ = child.close()
			requests.Wait()
			t.Fatal(err)
		}
	}
	cancel()
	if err := child.close(); err != nil {
		requests.Wait()
		t.Fatal(err)
	}
	requests.Wait()
	if child.cmd.ProcessState == nil {
		t.Fatal("owned child not reaped")
	}
	var exit *exec.ExitError
	if !errors.As(child.waitErr, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("cancelled child was not killed: %v", child.waitErr)
	}
	opened, err := wal.Open(filepath.Join(dir, "run"))
	if err != nil {
		t.Fatal("lock retained after cancellation", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeConcurrentCrashRejectsInvalidStoredOrder(t *testing.T) {
	p, _ := makeCrashPlan(17)
	c := p.Cases[0]
	g := wal.Genesis{RunID: crashRunID, Config: crashConfig()}
	inputs := append(append([]d.CheckpointCommand{}, c.Setup...), c.Attempts[0])
	records := []wal.Record{}
	for i, input := range inputs {
		cmd, err := input.Command(crashRunID)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, wal.Record{Seq: d.Int(i + 1), Command: cmd})
	}
	allowed := crashKeys(c, crashRunID)
	if _, err := crashModel(g, records, allowed); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"duplicate", "sequence", "payload", "prefix"} {
		t.Run(kind, func(t *testing.T) {
			bad := append([]wal.Record{}, records...)
			switch kind {
			case "duplicate":
				bad[2].Command = bad[1].Command
			case "sequence":
				bad[2].Seq = 4
			case "payload":
				bad[2].Command.Place.QuantityLots++
			case "prefix":
				bad[1], bad[0] = bad[0], bad[1]
				if crashPrefix(records, bad) == nil {
					t.Fatal("changed prefix accepted")
				}
				return
			}
			if _, err := crashModel(g, bad, allowed); err == nil {
				t.Fatal("invalid stored input accepted")
			}
		})
	}
}
