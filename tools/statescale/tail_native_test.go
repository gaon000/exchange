//go:build darwin && cgo

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/snapshot"
)

func TestNativeTailRecoveryCases(t *testing.T) {
	root := t.TempDir()
	for _, kind := range []string{"active", "history"} {
		dir := filepath.Join(root, kind)
		f, err := prepareTail(dir, kind, 24)
		if err != nil {
			t.Fatal(err)
		}
		receipt := filepath.Join(root, kind+".json")
		if err = save(receipt, f); err != nil {
			t.Fatal(err)
		}
		for _, cut := range tailCuts(24) {
			name, _ := snapshot.Filename(d.Int(cut))
			copy := filepath.Join(root, kind+"-"+name)
			if err = os.Mkdir(copy, 0700); err != nil {
				t.Fatal(err)
			}
			for _, file := range []string{".lock", "genesis.bin", "commands.wal", name} {
				b, e := os.ReadFile(filepath.Join(dir, file))
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(copy, file), b, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if err = command([]string{"tail-measure", "--kind", kind, "--count", "24", "--tail", strconv.Itoa(24 - cut), "--dir", copy, "--output", copy + ".json", "--receipt", receipt}); err == nil {
				t.Fatal("unowned tail run accepted")
			}
			if err = os.WriteFile(filepath.Join(copy, ".tail-owned"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			o, e := measureTail(copy, f, 24-cut)
			if e != nil || !o.Verified || o.FirstResponsesVerified != 8 || o.RetriesVerified != 2 || !o.OrdersBalancesTradeVerified {
				t.Fatal(kind, cut, e)
			}
			if _, e = measureTail(copy, f, 24-cut); e == nil && cut != 24 {
				t.Fatal("unexpected later snapshot accepted as original cut")
			}
		}
		for file, sha := range f.Files {
			got, e := digest(filepath.Join(dir, file))
			if e != nil || got != sha {
				t.Fatal("original tail fixture modified")
			}
		}
	}
}
