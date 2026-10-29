// benchreport verifies retained experiment evidence and applies fixed criteria.
// It never starts a server, modifies a run, or runs concurrently with a trial.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gaon000/exchange/internal/buildinfo"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string, stdout, stderr io.Writer) error {
	f := flag.NewFlagSet("benchreport", flag.ContinueOnError)
	f.SetOutput(stderr)
	criteriaFile := f.String("criteria", "config/benchmarks/criteria-v1.json", "fixed criteria and relative input references")
	output := f.String("output", "", "new report JSON outside all experiment bundles")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() < 1 || f.NArg() > 100 || *output == "" {
		return fmt.Errorf("one to 100 bundle directories and --output are required")
	}
	c, err := loadCriteria(*criteriaFile)
	if err != nil {
		return err
	}
	out, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(out))
	if err != nil {
		return err
	}
	out = filepath.Join(parent, filepath.Base(out))
	dirs := make([]string, f.NArg())
	seen := map[string]bool{}
	for i, name := range f.Args() {
		dirs[i], err = filepath.Abs(name)
		if err != nil {
			return err
		}
		dirs[i], err = filepath.EvalSymlinks(dirs[i])
		if err != nil {
			return err
		}
		if seen[dirs[i]] {
			return fmt.Errorf("duplicate bundle directory")
		}
		seen[dirs[i]] = true
		rel, err := filepath.Rel(dirs[i], out)
		if err != nil {
			return err
		}
		if filepath.IsLocal(rel) {
			return fmt.Errorf("report output must be outside bundle directories")
		}
	}
	file, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	r := report{Version: 1, CreatedUTC: time.Now().UTC(), Build: buildinfo.Current(), CriteriaSHA: c.hash, InputSHA: c.inputHash, Cases: make([]caseReport, 0, len(dirs))}
	for _, dir := range dirs {
		item, err := analyze(dir, c)
		if err != nil {
			item.Reasons = append(item.Reasons, "EVIDENCE: "+err.Error())
			item.Pass = false
		}
		r.Cases = append(r.Cases, item)
		// Retained plans/results are only needed for one case; release them
		// between runs so multi-bundle analysis has a bounded live working set.
		runtime.GC()
	}
	r.Groups = groupCases(r.Cases, c.MinimumRepeats)
	err = json.NewEncoder(file).Encode(r)
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "report recorded: %s (%d cases, %d groups); inspect pass/qualified fields\n", out, len(r.Cases), len(r.Groups))
	return err
}
