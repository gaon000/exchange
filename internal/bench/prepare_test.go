package bench

import (
	"testing"

	d "github.com/gaon000/exchange/internal/domain"
)

func TestPreparationRejectsInconsistentFundingAndCapacity(t *testing.T) {
	p := buildPlan(t, testSpec())
	var accounts []d.InitialAccount
	for _, id := range testSpec().Accounts {
		accounts = append(accounts, d.InitialAccount{AccountID: id, BaseLots: 1000000, QuoteAtoms: 1000000000})
	}
	accounts = append(accounts, d.InitialAccount{AccountID: testSpec().RejectAccount})
	config := d.DefaultConfig(accounts)
	if err := ValidatePreparation(p, config); err != nil {
		t.Fatal(err)
	}
	for name, modify := range map[string]func(*d.Config){
		"capacity": func(c *d.Config) { c.MaxCommands = d.Int(p.UniqueCommands() - 1) },
		"seed funding": func(c *d.Config) {
			for i := range c.Accounts {
				c.Accounts[i].BaseLots = 0
				c.Accounts[i].QuoteAtoms = 0
			}
		},
		"unregistered":   func(c *d.Config) { c.Accounts = c.Accounts[1:] },
		"reject funded":  func(c *d.Config) { c.Accounts[len(c.Accounts)-1].QuoteAtoms = 1 },
		"reject missing": func(c *d.Config) { c.Accounts = c.Accounts[:len(c.Accounts)-1] },
	} {
		t.Run(name, func(t *testing.T) {
			c := d.DefaultConfig(accounts)
			modify(&c)
			if err := ValidatePreparation(p, c); err == nil {
				t.Fatal("invalid preparation accepted")
			}
		})
	}
}
