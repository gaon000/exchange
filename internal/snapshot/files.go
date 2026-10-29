package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	d "github.com/gaon000/exchange/internal/domain"
)

// Candidate identifies one canonically named snapshot. Temporary and unrelated
// files are never candidates. The name does not establish validity or durability.
type Candidate struct {
	Name string
	Seq  d.Int
}

func (c Candidate) valid() bool {
	name, err := Filename(c.Seq)
	return err == nil && c.Name == name
}

// List returns snapshot names in descending sequence order. The caller must
// hold the run's ownership lock. Unexpected snapshot names stop recovery instead
// of allowing a potentially important file to disappear silently from selection.
func List(dir string) ([]Candidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]Candidate, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "snapshot-") || !strings.HasSuffix(name, ".bin") {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "snapshot-"), ".bin"), 10, 64)
		candidate := Candidate{Name: name, Seq: d.Int(n)}
		if err != nil || !candidate.valid() {
			return nil, corrupt("noncanonical snapshot filename: " + name)
		}
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Seq > result[j].Seq })
	return result, nil
}

// Load validates one candidate without altering or syncing it. A non-regular
// candidate or invalid envelope may be skipped; filesystem and close errors may
// not. Call Confirm only after state invariants and WAL inputs have been checked.
func Load(dir string, candidate Candidate, want Identity, maxSeq d.Int) (doc Document, err error) {
	if !candidate.valid() {
		return Document{}, ErrInvalid
	}
	path := filepath.Join(dir, candidate.Name)
	info, err := os.Lstat(path)
	if err != nil {
		return Document{}, err
	}
	if !info.Mode().IsRegular() {
		return Document{}, corrupt("candidate is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return Document{}, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			// Do not wrap a corruption sentinel alongside this I/O failure: the
			// caller must not downgrade a failed close to a fallback decision.
			doc = Document{}
			err = fmt.Errorf("snapshot close failed (read error: %v): %w", err, closeErr)
		}
	}()
	info, err = f.Stat()
	if err != nil {
		return Document{}, err
	}
	doc, err = Read(f, info.Size(), want, maxSeq)
	if err != nil {
		return Document{}, err
	}
	if doc.SnapshotSeq != candidate.Seq {
		return Document{}, corrupt("filename and payload sequence differ")
	}
	return doc, nil
}

// Confirm re-checks the selected canonical bytes and strictly syncs the existing
// file and its directory before readiness. It never recreates a missing file.
func Confirm(dir string, doc Document) error {
	return confirm(dir, doc, nativeFileOps())
}

func confirm(dir string, doc Document, ops fileOps) (err error) {
	size, err := measure(doc, MaxPayloadBytes)
	if err != nil {
		return err
	}
	if err := ops.checkFS(dir); err != nil {
		return err
	}
	name, _ := Filename(doc.SnapshotSeq)
	if exists, err := confirmIdentical(filepath.Join(dir, name), doc, size, ops); err != nil {
		return err
	} else if !exists {
		return corrupt("selected snapshot disappeared before confirmation")
	}
	directory, err := ops.open(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	return ops.sync(directory)
}
