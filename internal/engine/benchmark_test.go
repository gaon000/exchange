package engine_test

import (
	"fmt"
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/engine"
)

func BenchmarkEngine(b *testing.B) {
	for _, mode := range []string{"Rest", "Match", "Cancel", "Duplicate"} {
		b.Run(mode, func(b *testing.B) {
			c := d.DefaultConfig([]d.InitialAccount{{AccountID: "a", BaseLots: 1000000, QuoteAtoms: 1000000000}, {AccountID: "b", BaseLots: 1000000, QuoteAtoms: 1000000000}})
			e := newEngine(b, c)
			ids := make([]string, 512)
			for i := range ids {
				ids[i] = fmt.Sprint(i)
			}
			if mode == "Duplicate" {
				if _, err := e.Apply(1, place("a", "same", d.Buy, 100, 1)); err != nil {
					b.Fatal(err)
				}
			}
			run := func(cmd d.Command) {
				if _, err := e.Apply(e.AppliedSeq()+1, cmd); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Bound retained history. Periodic reset cost is included in ns/op.
				if mode != "Duplicate" && i > 0 && i%512 == 0 {
					var err error
					e, err = engine.New(runID, c)
					if err != nil {
						b.Fatal(err)
					}
				}
				id := ids[i%512]
				switch mode {
				case "Rest":
					run(place("a", id, d.Buy, 100, 1))
				case "Match":
					run(place("b", id, d.Sell, 100, 1))
					run(place("a", id, d.Buy, 100, 1))
				case "Cancel":
					run(place("a", id, d.Buy, 100, 1))
					run(cancel("a", id, id))
				case "Duplicate":
					run(place("a", "same", d.Buy, 100, 1))
				}
			}
			b.StopTimer()
			commands := float64(1)
			if mode == "Match" || mode == "Cancel" {
				commands = 2
			}
			if mode == "Duplicate" {
				commands = 0
			}
			b.ReportMetric(commands, "new_commands/op")
		})
	}
}
