package domain

// Checkpoint is the ordered v1 snapshot state, separate from diagnostic views.
// It contains owned copies; arrays are never nil in a canonical checkpoint.
type Checkpoint struct {
	Accounts []CheckpointAccount `json:"accounts"`
	Orders   []CheckpointOrder   `json:"orders"`
	Book     CheckpointBook      `json:"book"`
	Trades   []Trade             `json:"trades"`
	Results  []CheckpointResult  `json:"results"`
}

// CheckpointAccount records spendable and reserved holdings with explicit units.
type CheckpointAccount struct {
	AccountID           string `json:"accountId"`
	AvailableBaseLots   Int    `json:"availableBaseLots"`
	ReservedBaseLots    Int    `json:"reservedBaseLots"`
	AvailableQuoteAtoms Int    `json:"availableQuoteAtoms"`
	ReservedQuoteAtoms  Int    `json:"reservedQuoteAtoms"`
}

// CheckpointOrder includes its account scope and the complete current order.
type CheckpointOrder struct {
	AccountID string `json:"accountId"`
	Order     Order  `json:"order"`
}

// CheckpointBook records price priority and the FIFO references within each level.
type CheckpointBook struct {
	Bids []CheckpointLevel `json:"bids"`
	Asks []CheckpointLevel `json:"asks"`
}

// CheckpointLevel preserves the exact queue, rather than only its aggregate size.
type CheckpointLevel struct {
	PriceTicks Int        `json:"priceTicks"`
	Orders     []OrderRef `json:"orders"`
}

// CheckpointCommand is a versioned wire union; exactly one payload is present.
// Run identity is inherited from the snapshot envelope and restored explicitly.
type CheckpointCommand struct {
	AccountID string  `json:"accountId"`
	Kind      Kind    `json:"kind"`
	Place     *Place  `json:"place,omitempty"`
	Cancel    *Cancel `json:"cancel,omitempty"`
}

// Command validates and reconstructs the normalized in-process command.
func (c CheckpointCommand) Command(runID string) (Command, error) {
	command := Command{RunID: runID, AccountID: c.AccountID, Kind: c.Kind}
	switch {
	case c.Kind == PlaceOrder && c.Place != nil && c.Cancel == nil:
		command.Place = *c.Place
	case c.Kind == CancelOrder && c.Cancel != nil && c.Place == nil:
		command.Cancel = *c.Cancel
	default:
		return Command{}, ErrInvalid
	}
	return command, command.Validate()
}

// CheckpointResult preserves both the original input and its immutable response.
// Entries are ordered by accountId, kind, and requestId, not by response time.
type CheckpointResult struct {
	Command  CheckpointCommand `json:"command"`
	Response Result            `json:"response"`
}
