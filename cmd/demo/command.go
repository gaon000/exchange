package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/durable"
)

type options struct {
	Exchange, Output string
	Timeout          time.Duration
}

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("demo", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.StringVar(&o.Exchange, "exchange", "bin/exchange", "exchange binary to copy and run")
	f.StringVar(&o.Output, "output", "", "new output directory under an existing parent (default: unique var/demos directory)")
	f.DurationVar(&o.Timeout, "timeout", 2*time.Minute, "whole demo deadline, greater than zero and at most 10m")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || o.Exchange == "" || o.Timeout <= 0 || o.Timeout > 10*time.Minute {
		return o, errors.New("invalid arguments; use demo -h")
	}
	return o, nil
}

type summary struct {
	Status          string   `json:"status"`
	RunID           string   `json:"runId"`
	Output          string   `json:"output"`
	ServerSHA256    string   `json:"serverSHA256"`
	Checks          []string `json:"checks"`
	LastStage       string   `json:"lastStage"`
	Error           string   `json:"error,omitempty"`
	PowerLossTested bool     `json:"powerLossTested"`
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) (retErr error) {
	o, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("demo requires macOS arm64, APFS and CGO_ENABLED=1")
	}
	if err := durable.CheckFilesystem("."); err != nil {
		return err
	}
	dir, err := createOutput(o.Output)
	if err != nil {
		return err
	}
	s := summary{Status: "FAILED", Output: dir, Checks: []string{}, LastStage: "prepare"}
	var active *child
	var trace *os.File
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	defer func() {
		if active != nil {
			retErr = errors.Join(retErr, active.stop(10*time.Second))
		}
		if trace != nil {
			retErr = errors.Join(retErr, trace.Sync(), trace.Close())
		}
		retErr = errors.Join(retErr, ctx.Err())
		if retErr == nil {
			s.Status = "PASSED"
		} else {
			s.Error = retErr.Error()
		}
		retErr = errors.Join(retErr, writeJSON(filepath.Join(dir, "summary.json"), s))
		if retErr == nil {
			_, retErr = fmt.Fprintf(stdout, "PASS: 거래·정상 재시작·복구 확인 완료\n결과: %s\n전원 장애 검증: 보류 (이 데모에서 수행하지 않음)\n", dir)
		} else {
			retErr = fmt.Errorf("demo failed at %s; evidence preserved at %s: %w", s.LastStage, dir, retErr)
		}
	}()
	pass := func(label string) error {
		s.Checks = append(s.Checks, label)
		_, err := fmt.Fprintf(stdout, "[확인] %s\n", label)
		return err
	}
	if _, err := fmt.Fprintf(stdout, "거래 데모: %s\n", dir); err != nil {
		return err
	}
	binary := filepath.Join(dir, "exchange")
	s.ServerSHA256, err = copyBinary(o.Exchange, binary)
	if err != nil {
		return err
	}
	config := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", QuoteAtoms: 100_000_000_000}, {AccountID: "bob", BaseLots: 10_000}})
	config.MaxCommands = 3 // retries must still work after the unique-command limit
	configPath, dataDir := filepath.Join(dir, "config.json"), filepath.Join(dir, "run")
	if err := writeJSON(configPath, config); err != nil {
		return err
	}
	s.LastStage = "initialize"
	active, err = startChild([]string{binary, "init", "--config", configPath, "--data-dir", dataDir}, filepath.Join(dir, "init.json"), filepath.Join(dir, "init.stderr"))
	if err != nil {
		return err
	}
	if err := active.wait(ctx); err != nil {
		return err
	}
	active = nil
	initialized, err := readReport(filepath.Join(dir, "init.json"))
	if err != nil {
		return err
	}
	if initialized.Event != "initialized" || !d.ValidRunID(initialized.RunID) || initialized.DataDir != dataDir || initialized.ConfigHash == "" {
		return errors.New("invalid initialization report")
	}
	s.RunID = initialized.RunID
	trace, err = os.OpenFile(filepath.Join(dir, "http.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	startServer := func(number int, replayed d.Int) (*httpClient, error) {
		readyPath := filepath.Join(dir, fmt.Sprintf("server-%d.json", number))
		active, err = startChild([]string{binary, "serve", "--data-dir", dataDir, "--listen", "127.0.0.1:0"}, readyPath, filepath.Join(dir, fmt.Sprintf("server-%d.stderr", number)))
		if err != nil {
			return nil, err
		}
		r, err := awaitReady(ctx, active, readyPath)
		if err != nil {
			return nil, err
		}
		if r.RunID != s.RunID || r.ConfigHash != initialized.ConfigHash || r.DataDir != dataDir || r.Recovery.SnapshotSeq != 0 || r.Recovery.ReplayedCommands != replayed {
			return nil, errors.New("restart identity or replayed command count mismatch")
		}
		c := newHTTPClient("http://"+r.Listen, r.RunID, trace)
		_, err = expect(ctx, c, "alice", http.MethodGet, "/health/ready", nil, struct {
			State string `json:"state"`
		}{"READY"})
		return c, err
	}
	s.LastStage = "start"
	c, err := startServer(1, 0)
	if err != nil {
		return err
	}
	if err := checkState(ctx, c, 0); err != nil {
		return err
	}
	if err := pass("초기 잔고: Alice 100,000 USDT / Bob 1 BTC"); err != nil {
		return err
	}
	labels := []string{
		"주문: Alice 0.01 BTC 매수 @ 60,000 USDT → OPEN, 600 USDT 예약",
		"부분 체결: Bob 0.004 BTC 매도 @ 59,900 → 기존 매수 가격 60,000에 체결; Alice 잔량 0.006 BTC",
		"잔량 취소: Alice 0.006 BTC 취소 → 360 USDT 예약 해제",
	}
	var original [][]byte
	for i, cmd := range commands() {
		s.LastStage = fmt.Sprintf("command-%d", i+1)
		raw, err := expect(ctx, c, cmd.account, http.MethodPost, cmd.path, cmd.body, result(s.RunID, d.Int(i+1)))
		if err != nil {
			return err
		}
		original = append(original, raw)
		if err := checkState(ctx, c, d.Int(i+1)); err != nil {
			return err
		}
		if err := pass(labels[i]); err != nil {
			return err
		}
	}
	if err := checkResults(ctx, c, original, false); err != nil {
		return err
	}
	if err := pass("최종 잔고: Alice 0.004 BTC / 99,760 USDT, Bob 0.996 BTC / 240 USDT; 예약 잔고 모두 0"); err != nil {
		return err
	}
	s.LastStage = "restart"
	err = active.stop(10 * time.Second)
	active = nil
	if err != nil {
		return err
	}
	c, err = startServer(2, 3)
	if err != nil {
		return err
	}
	// Never retry a mutation until read-only queries have proved recovery.
	if err := checkState(ctx, c, 3); err != nil {
		return err
	}
	if err := checkResults(ctx, c, original, false); err != nil {
		return err
	}
	if err := pass("재시작·복구: 같은 runId, WAL 명령 3개, 주문·잔고·호가·체결·최초 응답 유지"); err != nil {
		return err
	}
	s.LastStage = "retry"
	if err := checkResults(ctx, c, original, true); err != nil {
		return err
	}
	if err := checkState(ctx, c, 3); err != nil {
		return err
	}
	if err := pass("중복 재시도: 최초 응답 동일, 명령 순번 3·잔고·체결 수 유지"); err != nil {
		return err
	}
	s.LastStage = "shutdown"
	err = active.stop(10 * time.Second)
	active = nil
	return err
}

func createOutput(path string) (string, error) {
	if path == "" {
		parent := filepath.Join("var", "demos")
		if err := os.MkdirAll(parent, 0700); err != nil {
			return "", err
		}
		if err := durable.CheckFilesystem(parent); err != nil {
			return "", err
		}
		path, err := os.MkdirTemp(parent, "demo-")
		if err != nil {
			return "", err
		}
		return filepath.Abs(path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := durable.CheckFilesystem(filepath.Dir(path)); err != nil {
		return "", err
	}
	return path, os.Mkdir(path, 0700)
}

func writeJSON(path string, value any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return errors.Join(json.NewEncoder(f).Encode(value), f.Sync(), f.Close())
}

func copyBinary(source, target string) (string, error) {
	info, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() > 128<<20 {
		return "", errors.New("exchange must be an executable regular file at most 128 MiB")
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, (128<<20)+1))
	if n > 128<<20 {
		err = errors.Join(err, errors.New("exchange binary grew beyond 128 MiB"))
	}
	return hex.EncodeToString(hash.Sum(nil)), errors.Join(err, out.Sync(), out.Close())
}
