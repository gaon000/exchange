package main

import (
	"fmt"
	d "github.com/gaon000/exchange/internal/domain"
	"math/rand"
)

var concurrentProfiles = []string{"cancel-race", "duplicate-storm", "id-conflict"}

func validProfile(profile string) bool {
	for _, name := range concurrentProfiles {
		if profile == name {
			return true
		}
	}
	return false
}

func profilePlan(seed int64, profile string) (plan, error) {
	if !validProfile(profile) {
		return plan{}, fmt.Errorf("unknown concurrent profile: %s", profile)
	}
	p, err := concurrentPlan(seed)
	if err != nil {
		return plan{}, err
	}
	p.Version, p.Profile = 3, profile
	rng := rand.New(rand.NewSource(seed))
	var actions []action
	for wave, index := range []int{2, 5} {
		maker, fill, g := p.Actions[index-2], p.Actions[index-1], p.Actions[index].Concurrent
		actions = append(actions, maker, fill)
		switch profile {
		case "cancel-race":
			// Cancellation can precede this newly created resting order and persist
			// ORDER_NOT_FOUND, or follow it and release its reservation. Both are valid.
			for _, list := range [][]concurrentAttempt{g.Initial, g.Retries} {
				for i := range list {
					if list[i].Command.Place != nil && list[i].Command.Place.ClientOrderID == g.Initial[0].Command.Place.ClientOrderID {
						c := clone(action{Command: &list[i].Command}).Command
						c.Place.PriceTicks--
						list[i].Command = *c
					}
					if list[i].Command.Cancel != nil {
						c := clone(action{Command: &list[i].Command}).Command
						c.AccountID = "bob"
						c.Cancel.ClientOrderID = g.Initial[0].Command.Place.ClientOrderID
						list[i].Command = *c
					}
				}
			}
		case "duplicate-storm":
			initial := append([]concurrentAttempt{}, g.Initial[:2]...)
			for i := 2; i < 10; i++ {
				initial = append(initial, concurrentAttempt{Client: i, Command: g.Initial[0].Command})
			}
			c, h := g.Initial[2], g.Initial[3]
			c.Client, h.Client = 10, 11
			g.Initial = append(initial, c, h)
			retry := []concurrentAttempt{}
			for i := 0; i < 8; i++ {
				retry = append(retry, concurrentAttempt{Client: i, Command: g.Retries[0].Command})
			}
			for i, a := range []concurrentAttempt{g.Retries[1], g.Retries[2], g.Retries[3], g.Retries[3]} {
				a.Client = 8 + i
				retry = append(retry, a)
			}
			g.Retries = retry
		case "id-conflict":
			knownCancel := cancelOrder("alice", fmt.Sprintf("parallel-%d-known-cancel", wave), "missing")
			actions = append(actions, knownCancel)
			for i := 4; i < 8; i++ {
				input := maker
				if i < 6 {
					input = clone(input)
					input.Command.Place.QuantityLots += d.Int(i - 3)
				} else {
					input = clone(knownCancel)
					input.Command.Cancel.ClientOrderID = fmt.Sprintf("different-%d", i)
				}
				a := concurrentAttempt{Client: i, Command: *input.Command, ExpectConflict: true}
				g.Initial = append(g.Initial, a)
				g.Retries = append(g.Retries, a)
			}
		}
		// Roles are not tied to one client ID. The witness role follows the
		// permutation so snapshot/replay gates retain their original meaning.
		ids := rng.Perm(len(g.Initial))
		oldMarker := g.SnapshotClient
		for i := range g.Initial {
			g.Initial[i].Client = ids[g.Initial[i].Client]
		}
		for i := range g.Retries {
			g.Retries[i].Client = ids[g.Retries[i].Client]
		}
		g.SnapshotClient = ids[oldMarker]
		actions = append(actions, action{Kind: "concurrent", Concurrent: g})
	}
	p.Actions = append(actions, action{Kind: "restart"})
	return p, validatePlan(p)
}
