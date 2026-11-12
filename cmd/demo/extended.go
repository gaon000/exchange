package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type recoveryCheck struct {
	Server              int   `json:"server"`
	SnapshotSeq         d.Int `json:"snapshotSeq"`
	ReplayedCommands    d.Int `json:"replayedCommands"`
	VerifiedBeforeRetry bool  `json:"verifiedBeforeRetry"`
}

func runExtended(ctx context.Context, o options, stdout io.Writer) (retErr error) {
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
	s := summary{Scenario: o.Scenario, Status: "FAILED", Output: dir, Checks: []string{}, LastStage: "prepare"}
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
			_, retErr = fmt.Fprintf(stdout, "PASS: 거래 규칙·%s 복구 확인 완료\n결과: %s\n전원 장애 검증: 보류 (이 데모에서 수행하지 않음)\n", o.Scenario, dir)
		} else {
			retErr = fmt.Errorf("demo failed at %s; evidence preserved at %s: %w", s.LastStage, dir, retErr)
		}
	}()
	pass := func(label string) error {
		s.Checks = append(s.Checks, label)
		_, err := fmt.Fprintf(stdout, "[확인] %s\n", label)
		return err
	}
	if _, err := fmt.Fprintf(stdout, "거래·복구 데모 (%s): %s\n", o.Scenario, dir); err != nil {
		return err
	}
	binary := filepath.Join(dir, "exchange")
	s.ServerSHA256, err = copyBinary(o.Exchange, binary)
	if err != nil {
		return err
	}
	config := d.DefaultConfig([]d.InitialAccount{{AccountID: "alice", QuoteAtoms: 100_000_000_000}, {AccountID: "bob", BaseLots: 10_000}})
	config.MaxCommands = 8
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
	var original [][]byte
	number := 0
	startServer := func(snap, replayed d.Int, schedule bool) (*httpClient, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		number++
		s.LastStage = fmt.Sprintf("start-server-%d", number)
		readyPath := filepath.Join(dir, fmt.Sprintf("server-%d.json", number))
		args := []string{binary, "serve", "--data-dir", dataDir, "--listen", "127.0.0.1:0"}
		if schedule {
			args = append(args, "--snapshot-interval", "100ms", "--snapshot-log", filepath.Join(dir, "snapshot-events.jsonl"))
		}
		active, err = startChild(args, readyPath, filepath.Join(dir, fmt.Sprintf("server-%d.stderr", number)))
		if err != nil {
			return nil, err
		}
		r, err := awaitReady(ctx, active, readyPath)
		if err != nil {
			return nil, err
		}
		if r.RunID != s.RunID || r.ConfigHash != initialized.ConfigHash || r.DataDir != dataDir || r.Recovery.SnapshotSeq != snap || r.Recovery.ReplayedCommands != replayed || (snap > 0 && r.Recovery.SnapshotFile == "") {
			return nil, errors.New("restart identity or snapshot/replay boundary mismatch")
		}
		c := newHTTPClient("http://"+r.Listen, s.RunID, trace)
		if _, err := expect(ctx, c, "alice", http.MethodGet, "/health/ready", nil, struct {
			State string `json:"state"`
		}{"READY"}); err != nil {
			return nil, err
		}
		// No POST, including a retry, may hide a lost result or reservation.
		if err := checkExtendedState(ctx, c, snap+replayed); err != nil {
			return nil, err
		}
		if err := checkExtendedResults(ctx, c, original, false); err != nil {
			return nil, err
		}
		if number > 1 {
			s.Recoveries = append(s.Recoveries, recoveryCheck{number, snap, replayed, true})
		}
		return c, nil
	}
	stop := func() error { err := active.stop(10 * time.Second); active = nil; return err }
	c, err := startServer(0, 0, o.Scenario == "snapshot")
	if err != nil {
		return err
	}
	if err := pass("초기 잔고: Alice 100,000 USDT / Bob 1 BTC"); err != nil {
		return err
	}
	execute := func(seq d.Int) error {
		s.LastStage = fmt.Sprintf("command-%d", seq)
		cmd := extendedCommands()[int(seq)-1]
		raw, err := expect(ctx, c, cmd.account, http.MethodPost, cmd.path, cmd.body, extendedResult(s.RunID, seq))
		if err != nil {
			return err
		}
		original = append(original, raw)
		return checkExtendedState(ctx, c, seq)
	}
	labels := []string{
		"주문: Alice 0.01 BTC 매수 → OPEN, 600 USDT 예약",
		"부분 체결: 0.004 BTC, 기존 매수 가격 60,000에 체결",
		"잔량 취소: 360 USDT 예약 해제",
		"대기 매도: Bob 0.001 BTC @ 50,000, 0.001 BTC 예약",
		"자기 거래 차단: Bob 신규 매수 잔량 취소, 50 USDT 예약 반환; 기존 매도 유지",
		"가격 개선: Alice 60 USDT 예약 중 50 USDT 체결·10 USDT 반환, 예약 0",
	}
	for i, label := range labels {
		if err := execute(d.Int(i + 1)); err != nil {
			return err
		}
		if err := pass(label); err != nil {
			return err
		}
	}
	s.LastStage = "conflict-before-restart"
	if err := checkConflicts(ctx, c, 6, original); err != nil {
		return err
	}
	if err := pass("중복 ID 충돌: 주문·취소의 다른 내용은 409, 순번·잔고·최초 결과 유지"); err != nil {
		return err
	}
	if o.Scenario == "all" {
		s.LastStage = "wal-restart"
		if err := stop(); err != nil {
			return err
		}
		c, err = startServer(0, 6, true)
		if err != nil {
			return err
		}
		if err := pass("정상 WAL 복구: 명령 6개, 상태·최초 응답 GET 검증"); err != nil {
			return err
		}
	}
	snap := d.Int(0)
	if o.Scenario != "crash" {
		s.LastStage = "snapshot-publication"
		if err := awaitSnapshot(ctx, active, filepath.Join(dir, "snapshot-events.jsonl"), s.RunID, 6); err != nil {
			return err
		}
		if err := pass("스냅샷 게시: snapshotSeq=6 완료 이벤트 확인"); err != nil {
			return err
		}
		s.LastStage = "snapshot-restart"
		if err := stop(); err != nil {
			return err
		}
		c, err = startServer(6, 0, false)
		if err != nil {
			return err
		}
		snap = 6
		if err := pass("스냅샷 복구: snapshotSeq=6·WAL 재생 0, 재시도 전 전체 조회 검증"); err != nil {
			return err
		}
	}
	if err := execute(7); err != nil {
		return err
	}
	if err := pass("WAL tail: 새 매수 OPEN, 20 USDT 예약 유지"); err != nil {
		return err
	}
	s.LastStage = "tail-restart"
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.Scenario == "snapshot" {
		if err := stop(); err != nil {
			return err
		}
	} else {
		if err := active.kill(); err != nil {
			return err
		}
		active = nil
		s.ProcessKillTested = true
		if err := pass("프로세스 강제 종료: 데모 소유 서버 SIGKILL·Wait 완료"); err != nil {
			return err
		}
	}
	c, err = startServer(snap, 7-snap, false)
	if err != nil {
		return err
	}
	if err := pass(fmt.Sprintf("복구: snapshotSeq=%d·WAL 재생 %d, 주문·20 USDT 예약·최초 응답 유지", snap, 7-snap)); err != nil {
		return err
	}
	if err := execute(8); err != nil {
		return err
	}
	if err := pass("복구 후 취소: 20 USDT 예약 해제; Alice 0.005 BTC / 99,710 USDT, Bob 0.995 BTC / 290 USDT"); err != nil {
		return err
	}
	s.LastStage = "retry-after-recovery"
	if err := checkExtendedResults(ctx, c, original, false); err != nil {
		return err
	}
	if err := checkExtendedResults(ctx, c, original, true); err != nil {
		return err
	}
	if err := checkConflicts(ctx, c, 8, original); err != nil {
		return err
	}
	if err := pass("중복 재시도: 8개 최초 응답 bytes 동일, 한도 도달 후 충돌/재시도·상태 불변"); err != nil {
		return err
	}
	s.LastStage = "shutdown"
	return stop()
}

func awaitSnapshot(ctx context.Context, p *child, path, run string, seq d.Int) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-p.done:
			return errors.Join(errors.New("server exited before snapshot"), p.err)
		default:
		}
		f, err := os.Open(path)
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
			err = errors.Join(readErr, f.Close())
			if err != nil {
				return err
			}
			if len(data) > 1<<20 {
				return errors.New("snapshot event log exceeded 1 MiB")
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			for {
				var event struct {
					RunID       string    `json:"runId"`
					SnapshotSeq d.Int     `json:"snapshotSeq"`
					Outcome     string    `json:"outcome"`
					FinishedUTC time.Time `json:"finishedUTC"`
					Error       string    `json:"error"`
				}
				err = decoder.Decode(&event)
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
					break
				}
				if err != nil {
					return fmt.Errorf("invalid snapshot event: %w", err)
				}
				if event.RunID != run || event.Outcome != "COMPLETED" || event.FinishedUTC.IsZero() || event.Error != "" {
					return errors.New("snapshot publication identity/outcome mismatch")
				}
				if event.SnapshotSeq == seq {
					return nil
				}
				if event.SnapshotSeq > seq {
					return errors.New("snapshot passed expected sequence")
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
