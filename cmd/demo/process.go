package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

// Only this directly created child is signaled. Logs are files, not unbounded pipes.
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
	err := p.cmd.Process.Signal(syscall.SIGTERM)
	if errors.Is(err, os.ErrProcessDone) {
		err = nil
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return errors.Join(err, p.err)
	case <-timer.C:
		killErr := p.cmd.Process.Kill()
		<-p.done
		return errors.Join(fmt.Errorf("own child did not stop within %s; killed", timeout), err, killErr, p.err)
	}
}

func (p *child) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err() // caller's cleanup stops and joins the child
	}
}

type serverReport struct {
	Event, RunID, ConfigHash, DataDir, Listen string
	Recovery                                  struct {
		SnapshotSeq, ReplayedCommands d.Int
	}
}

func readReport(path string) (serverReport, error) {
	var r serverReport
	f, err := os.Open(path)
	if err != nil {
		return r, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return r, err
	}
	if len(b) > 1<<20 {
		return r, errors.New("server report exceeded 1 MiB")
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

func awaitReady(ctx context.Context, p *child, path string) (serverReport, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return serverReport{}, ctx.Err()
		case <-p.done:
			return serverReport{}, errors.Join(errors.New("server exited before readiness"), p.err)
		default:
		}
		if r, err := readReport(path); err == nil && r.Event == "ready" {
			host, port, err := net.SplitHostPort(r.Listen)
			n, portErr := strconv.Atoi(port)
			if err != nil || portErr != nil || n < 1 || n > 65535 || host != "127.0.0.1" || !d.ValidRunID(r.RunID) {
				return r, errors.New("server readiness must contain a valid run ID and numeric IPv4 loopback port")
			}
			return r, nil
		}
		select {
		case <-ctx.Done():
			return serverReport{}, ctx.Err()
		case <-p.done:
			return serverReport{}, errors.Join(errors.New("server exited before readiness"), p.err)
		case <-tick.C:
		}
	}
}
