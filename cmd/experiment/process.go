package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Only directly created children are signaled. stdout/stderr go straight to
// files, so no pipe-reading goroutine can outlive Wait or grow an output buffer.
func startChild(argv []string, stdout, stderr string) (*child, error) {
	out, err := os.OpenFile(stdout, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	errout, err := os.OpenFile(stderr, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.Join(err, out.Close())
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = out, errout
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, out.Close(), errout.Close())
	}
	p := &child{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = errors.Join(cmd.Wait(), out.Sync(), out.Close(), errout.Sync(), errout.Close())
		close(p.done)
	}()
	return p, nil
}

func (p *child) stop(timeout time.Duration) error {
	select {
	case <-p.done:
		return p.err
	default:
	}
	signalErr := p.cmd.Process.Signal(syscall.SIGTERM)
	if errors.Is(signalErr, os.ErrProcessDone) {
		signalErr = nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return errors.Join(signalErr, p.err)
	case <-timer.C:
		killErr := p.cmd.Process.Kill()
		<-p.done
		return errors.Join(fmt.Errorf("child failed to drain within %s; killed own process", timeout), signalErr, killErr, p.err)
	}
}
func (p *child) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return errors.Join(ctx.Err(), p.stop(30*time.Second))
	}
}

type ready struct {
	Event      string `json:"event"`
	RunID      string `json:"runId"`
	DataDir    string `json:"dataDir"`
	ConfigHash string `json:"configHash"`
}

func awaitReady(ctx context.Context, p *child, path string) (ready, error) {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var r ready
		if err := readJSON(path, &r); err == nil && r.Event == "ready" && r.RunID != "" {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-p.done:
			return r, errors.Join(fmt.Errorf("server exited before readiness"), p.err)
		case <-deadline.C:
			return r, fmt.Errorf("readiness timeout")
		case <-ticker.C:
		}
	}
}
