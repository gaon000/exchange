package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
	"github.com/gaon000/exchange/internal/strictjson"
)

type crashOrder struct {
	BeforeKill []d.CheckpointCommand  `json:"beforeKill"`
	AfterRetry *[]d.CheckpointCommand `json:"afterRetry,omitempty"`
}
type crashCase struct {
	Boundary string                `json:"boundary"`
	Setup    []d.CheckpointCommand `json:"setup"`
	Attempts []d.CheckpointCommand `json:"attempts"`
	Tail     *d.CheckpointCommand  `json:"tail,omitempty"`
	Recorded *crashOrder           `json:"recorded,omitempty"`
}
type crashPlan struct {
	Version int         `json:"version"`
	Seed    int64       `json:"seed"`
	Cases   []crashCase `json:"cases"`
}

const crashRunID = "00000000-0000-4000-8000-000000000001"

var crashBoundaries = []string{"wal-after-sync", "snapshot-before-publish", "snapshot-after-publish"}

func crashConfig() d.Config {
	c := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", BaseLots: 100, QuoteAtoms: 100000}, {AccountID: "bob", BaseLots: 100, QuoteAtoms: 100000}, {AccountID: "carol", BaseLots: 100, QuoteAtoms: 100000}})
	c.MaxCommands = 32
	return c
}
func crashPlace(account, id string, side d.Side, price, qty d.Int) d.CheckpointCommand {
	return d.CheckpointCommand{AccountID: account, Kind: d.PlaceOrder, Place: &d.Place{ClientOrderID: id, Symbol: d.Symbol, Side: side, Type: "LIMIT", TimeInForce: "GTC", PriceTicks: price, QuantityLots: qty}}
}
func makeCrashPlan(seed int64) (crashPlan, error) {
	if seed < 0 || seed > 1<<31-1 {
		return crashPlan{}, errors.New("seed must be 0..2147483647")
	}
	rng := rand.New(rand.NewSource(seed))
	p := crashPlan{Version: 1, Seed: seed}
	for _, boundary := range crashBoundaries {
		buy := crashPlace("bob", "buy", d.Buy, 100, d.Int(1+rng.Intn(3)))
		c := crashCase{Boundary: boundary,
			Setup:    []d.CheckpointCommand{crashPlace("alice", "maker", d.Sell, 100, 8), crashPlace("bob", "fill", d.Buy, 100, 1)},
			Attempts: []d.CheckpointCommand{buy, buy, {AccountID: "alice", Kind: d.CancelOrder, Cancel: &d.Cancel{CancelRequestID: "cancel", ClientOrderID: "maker"}}, crashPlace("carol", "rest", d.Buy, 90, 3)},
		}
		if boundary != "wal-after-sync" {
			tail := crashPlace("bob", "tail", d.Sell, 90, 1)
			c.Tail = &tail
		}
		p.Cases = append(p.Cases, c)
	}
	return p, nil
}
func validateCrashPlan(p crashPlan) error {
	want, err := makeCrashPlan(p.Seed)
	if err != nil {
		return err
	}
	if p.Version != 1 || len(p.Cases) != 3 {
		return errors.New("expected version1 and exactly three bounded cases")
	}
	for i, c := range p.Cases {
		plain := c
		plain.Recorded = nil
		if !reflect.DeepEqual(plain, want.Cases[i]) {
			return errors.New("unsupported boundary or altered fixture inputs")
		}
		if c.Recorded == nil {
			continue
		}
		expected := map[d.Key]d.Command{}
		for _, input := range c.Attempts {
			cmd, err := input.Command(crashRunID)
			if err != nil {
				return err
			}
			expected[cmd.Key()] = cmd
		}
		n := 3
		if c.Boundary == "wal-after-sync" {
			n = 1
		}
		if len(c.Recorded.BeforeKill) != n {
			return errors.New("invalid recorded crash prefix length")
		}
		seen := map[d.Key]bool{}
		lists := [][]d.CheckpointCommand{c.Recorded.BeforeKill}
		if c.Recorded.AfterRetry != nil {
			if len(*c.Recorded.AfterRetry) != 3-n {
				return errors.New("invalid recorded retry order length")
			}
			lists = append(lists, *c.Recorded.AfterRetry)
		}
		for _, list := range lists {
			for _, input := range list {
				cmd, err := input.Command(crashRunID)
				if err != nil || expected[cmd.Key()] != cmd || seen[cmd.Key()] {
					return errors.New("unexpected or duplicate recorded key/payload")
				}
				seen[cmd.Key()] = true
			}
		}
	}
	return nil
}
func crashRead(path string, max int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("expected bounded regular input")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if int64(len(b)) > max {
		return nil, errors.New("input grew beyond limit")
	}
	return b, err
}
func crashJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return errors.Join(err, f.Sync(), f.Close())
}
func crashHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
func crashFresh(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return "", errors.New("output must not exist")
	}
	for parent := filepath.Dir(abs); ; parent = filepath.Dir(parent) {
		st, err := os.Lstat(parent)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("output parents must be existing real directories")
		}
		for _, name := range []string{"genesis.bin", "commands.wal", "scenario.json", "manifest.json", "artifact-manifest.json", "build.json", "package-index.json", "restore-index.json"} {
			if _, err := os.Lstat(filepath.Join(parent, name)); err == nil {
				return "", errors.New("output is inside existing evidence/run")
			}
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return abs, nil
}
func crashQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func crashReproduction(output string) string {
	path := filepath.Join(output, "scenario.json")
	entries, _ := os.ReadDir(output)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "replay-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := crashRead(filepath.Join(output, e.Name()), 1<<20)
		var p crashPlan
		if err == nil && strictjson.Decode(b, &p) == nil && validateCrashPlan(p) == nil {
			path = filepath.Join(output, e.Name())
		}
	}
	return "sh scripts/concurrent-crash.sh --replay " + crashQuote(path)
}
func crashSeal(output string) error {
	files := map[string]any{}
	if err := filepath.WalkDir(output, func(path string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		st, err := e.Info()
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return errors.New("nonregular evidence")
		}
		hash, err := crashHash(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(output, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = map[string]any{"bytes": st.Size(), "sha256": hash}
		return nil
	}); err != nil {
		return err
	}
	if err := crashJSON(filepath.Join(output, "manifest.json"), map[string]any{"version": 1, "files": files}); err != nil {
		return err
	}
	hash, err := crashHash(filepath.Join(output, "manifest.json"))
	if err != nil {
		return err
	}
	return crashJSON(filepath.Join(output, "completed.json"), map[string]any{"status": "PASSED", "manifestSHA256": hash, "powerLossTested": false, "processKillTested": true})
}
func parseCrashCommand(args []string, stderr io.Writer) (crashPlan, string, string, error) {
	fs := flag.NewFlagSet("concurrent-crash", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "fixture seed, 0..2147483647")
	replay := fs.String("replay", "", "saved scenario/replay JSON; no seed with replay")
	output := fs.String("output", "", "fresh directory under existing parent")
	if err := fs.Parse(args); err != nil {
		return crashPlan{}, "", "", err
	}
	if fs.NArg() != 0 {
		return crashPlan{}, "", "", errors.New("unexpected positional arguments")
	}
	var p crashPlan
	var err error
	if *replay != "" {
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "seed" {
				err = errors.New("replay cannot be combined with seed")
			}
		})
		if err != nil {
			return p, "", "", err
		}
		var b []byte
		b, err = crashRead(*replay, 1<<20)
		if err == nil {
			err = strictjson.Decode(b, &p)
		}
	} else {
		p, err = makeCrashPlan(*seed)
	}
	if err == nil {
		err = validateCrashPlan(p)
	}
	if err != nil {
		return p, "", "", err
	}
	if *output == "" {
		*output = filepath.Join("var", "crash-scenarios", time.Now().UTC().Format("20060102T150405.000000000"))
	}
	abs, err := crashFresh(*output)
	if err != nil {
		return p, "", "", err
	}
	if *replay != "" {
		input, err := filepath.Abs(*replay)
		if err != nil {
			return p, "", "", err
		}
		rel, err := filepath.Rel(filepath.Dir(input), abs)
		if err != nil || rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p, "", "", errors.New("output must be outside replay evidence")
		}
	}
	return p, abs, *replay, nil
}
func TestConcurrentCrashCommand(t *testing.T) {
	switch os.Getenv("EXCHANGE_CONCURRENT_CRASH_MODE") {
	case "child":
		crashChild(t)
	case "command":
		p, out, _, err := parseCrashCommand(flag.Args(), os.Stderr)
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		if err == nil {
			err = runCrashSuite(p, out, os.Stdout)
		}
		if err != nil {
			t.Fatal(err)
		}
	default:
		_, _, _, err := parseCrashCommand([]string{"--help"}, io.Discard)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatal(err)
		}
	}
}
func TestConcurrentCrashPlanAndInputProtection(t *testing.T) {
	for _, seed := range []int64{0, 17, 42, 701, 1<<31 - 1} {
		p, err := makeCrashPlan(seed)
		if err != nil || validateCrashPlan(p) != nil {
			t.Fatal(seed, err)
		}
		for i, c := range p.Cases {
			order := []d.CheckpointCommand{c.Attempts[0], c.Attempts[2], c.Attempts[3]}
			n := 3
			if i == 0 {
				n = 1
			}
			retry := append([]d.CheckpointCommand{}, order[n:]...)
			p.Cases[i].Recorded = &crashOrder{BeforeKill: order[:n], AfterRetry: &retry}
		}
		if err := validateCrashPlan(p); err != nil {
			t.Fatal(err)
		}
		p.Cases[0].Recorded.BeforeKill = append(p.Cases[0].Recorded.BeforeKill, p.Cases[0].Attempts[0])
		if validateCrashPlan(p) == nil {
			t.Fatal("duplicate prefix accepted")
		}
	}
	for _, args := range [][]string{{"--seed=-1"}, {"--seed=2147483648"}, {"--seed=17", "--replay=missing"}, {"unexpected"}} {
		if _, _, _, err := parseCrashCommand(args, io.Discard); err == nil {
			t.Fatal(args)
		}
	}
	dir := t.TempDir()
	if _, err := crashFresh(dir); err == nil {
		t.Fatal("reused output")
	}
	if err := os.WriteFile(filepath.Join(dir, "scenario.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := crashFresh(filepath.Join(dir, "new")); err == nil {
		t.Fatal("nested evidence output")
	}
	p, _ := makeCrashPlan(17)
	if err := crashJSON(filepath.Join(dir, "valid.json"), p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "replay-999.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(crashReproduction(dir), "replay-999") {
		t.Fatal("partial replay selected")
	}
}

func runCrashSuite(p crashPlan, output string, stdout io.Writer) (err error) {
	if err := validateCrashPlan(p); err != nil {
		return err
	}
	output, err = crashFresh(output)
	if err != nil {
		return err
	}
	if err := durable.CheckFilesystem(filepath.Dir(output)); err != nil {
		return err
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	defer func() {
		if problem := recover(); problem != nil {
			err = fmt.Errorf("panic: %v", problem)
		}
		if err != nil {
			reproduce := crashReproduction(output)
			err = errors.Join(err, crashJSON(filepath.Join(output, "failed.json"), map[string]any{"status": "FAILED", "error": err.Error(), "reproduce": reproduce, "powerLossTested": false}))
			fmt.Fprintf(stdout, "FAILED: %s\nReproduce: %s\n", output, reproduce)
		}
	}()
	if err := crashJSON(filepath.Join(output, "scenario.json"), p); err != nil {
		return err
	}
	if err := crashJSON(filepath.Join(output, "config.json"), crashConfig()); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	hash, err := crashHash(executable)
	if err != nil {
		return err
	}
	build := map[string]any{"goVersion": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH, "testBinarySHA256": hash, "testOnlyHooks": true}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if strings.HasPrefix(s.Key, "vcs.") {
				build[s.Key] = s.Value
			}
		}
	}
	if err := crashJSON(filepath.Join(output, "build.json"), build); err != nil {
		return err
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	if err := executeCrashSuite(ctx, p, output); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "PASSED seed=%d boundaries=3\nEvidence: %s\nReproduce: %s\n", p.Seed, output, crashReproduction(output)); err != nil {
		return err
	}
	return crashSeal(output)
}
