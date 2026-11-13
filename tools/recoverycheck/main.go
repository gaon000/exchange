// recoverycheck runs bounded, reproducible HTTP trading and disk recovery scenarios.
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
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/gaon000/exchange/internal/durable"
	"github.com/gaon000/exchange/internal/strictjson"
)

func main() {
	if err := command(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("recoverycheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "deterministic generator seed")
	count := fs.Int("actions", 64, "32..256 actions, including mandatory prefix and final restart")
	replay := fs.String("replay", "", "saved scenario.json; generator flags cannot be combined")
	output := fs.String("output", "", "fresh output directory; parent must exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	var p plan
	var err error
	if *replay != "" {
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "seed" || f.Name == "actions" {
				err = errors.New("replay cannot be combined with seed/actions")
			}
		})
		if err != nil {
			return err
		}
		data, readErr := readRegular(*replay, 1<<20)
		if readErr != nil {
			return readErr
		}
		if err := strictjson.Decode(data, &p); err != nil {
			return err
		}
	} else {
		p, err = generate(*seed, *count)
		if err != nil {
			return err
		}
	}
	if *output == "" {
		*output = filepath.Join("var", "scenarios", time.Now().UTC().Format("20060102T150405.000000000"))
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	return run(ctx, p, *output, *replay, stdout)
}

func fresh(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); !os.IsNotExist(err) {
		return "", errors.New("output must not exist, even if empty")
	}
	for parent := filepath.Dir(abs); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("output parents must be existing real directories")
		}
		for _, name := range []string{"genesis.bin", "commands.wal", "manifest.json", "artifact-manifest.json", "scenario.json", "build.json", "package-index.json", "restore-index.json"} {
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

func readRegular(path string, max int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("input must be a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if len(b) > int(max) {
		return nil, errors.New("input exceeded limit while reading")
	}
	return b, err
}

func writeJSON(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(b, '\n'))
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func run(ctx context.Context, p plan, output, replay string, stdout io.Writer) (err error) {
	if err := validatePlan(p); err != nil {
		return err
	}
	output, err = fresh(output)
	if err != nil {
		return err
	}
	// The storage adapter refuses unsupported platforms before any output writes.
	if err := durable.CheckFilesystem(filepath.Dir(output)); err != nil {
		return err
	}
	if replay != "" {
		input, err := filepath.Abs(replay)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(input), output)
		if err != nil || rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return errors.New("output must be outside replay evidence directory")
		}
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	reproduce := "sh scripts/random-recovery.sh --replay " + shellQuote(filepath.Join(output, "scenario.json"))
	step := -1
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
		if err != nil {
			writeErr := writeJSON(filepath.Join(output, "failed.json"), map[string]any{"status": "FAILED", "step": step, "error": err.Error(), "reproduce": reproduce, "powerLossTested": false})
			err = errors.Join(err, writeErr)
			fmt.Fprintf(stdout, "FAILED: %s\nReproduce: %s\n", output, reproduce)
		}
	}()
	if err := writeJSON(filepath.Join(output, "scenario.json"), p); err != nil {
		return err
	}
	build := map[string]any{"goVersion": runtime.Version(), "goos": runtime.GOOS, "goarch": runtime.GOARCH}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	binaryHash, err := hashFile(executable)
	if err != nil {
		return err
	}
	build["binarySHA256"] = binaryHash
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if strings.HasPrefix(setting.Key, "vcs.") {
				build[setting.Key] = setting.Value
			}
		}
	}
	if err := writeJSON(filepath.Join(output, "build.json"), build); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(output, "config.json"), config()); err != nil {
		return err
	}
	if err := execute(ctx, p, output, &step, reproduce); err != nil {
		return err
	}
	// execute has closed the runtime and trace files before sealing. Never
	// publish a completion while deferred shutdown/write errors remain pending.
	files := map[string]any{}
	if err := filepath.WalkDir(output, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.Join(err, errors.New("nonregular evidence file"))
		}
		hash, err := hashFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(output, path)
		files[filepath.ToSlash(name)] = map[string]any{"bytes": info.Size(), "sha256": hash}
		return err
	}); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(output, "manifest.json"), map[string]any{"version": 1, "files": files}); err != nil {
		return err
	}
	hash, err := hashFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "PASSED seed=%d actions=%d\nEvidence: %s\nReproduce: %s\n", p.Seed, len(p.Actions), output, reproduce); err != nil {
		return err
	}
	return writeJSON(filepath.Join(output, "completed.json"), map[string]any{"version": 1, "status": "PASSED", "manifestSHA256": hash, "powerLossTested": false})
}
