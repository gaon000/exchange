package runtime

import (
	d "github.com/gaon000/exchange/internal/domain"
	"time"
)

func validateQuery(q Query) error {
	switch q.Kind {
	case OrderQuery:
		if !d.ValidID(q.ID) {
			return d.ErrInvalid
		}
	case BalancesQuery:
	case BookQuery:
		if q.Limit < 1 || q.Limit > 100 {
			return d.ErrInvalid
		}
	case TradesQuery:
		if q.Limit < 1 || q.Limit > 1000 || q.AfterSeq < 0 || q.AfterIndex < 0 || (q.AfterSeq == 0) != (q.AfterIndex == 0) || q.AfterSeq > 0 && q.ThroughSeq == nil || q.ThroughSeq != nil && (*q.ThroughSeq < 0 || q.AfterSeq > *q.ThroughSeq) {
			return d.ErrInvalid
		}
	case ResultQuery:
		if !d.ValidID(q.ID) || (q.RequestKind != d.PlaceOrder && q.RequestKind != d.CancelOrder) {
			return d.ErrInvalid
		}
	default:
		return d.ErrInvalid
	}
	return nil
}

func (r *Runtime) readOwner(req *queryRequest, pending map[d.Key]*pendingCommand) {
	if !req.queuedAt.IsZero() {
		r.metrics.queryQueue.Observe(time.Since(req.queuedAt))
	}
	started := time.Now()
	defer func() { r.metrics.query.Observe(time.Since(started)) }()
	q := req.query
	v := View{RunID: r.genesis.RunID, AsOfSeq: r.engine.AppliedSeq()}
	var err error
	switch q.Kind {
	case OrderQuery:
		if o, ok := r.engine.Order(q.AccountID, q.ID); ok {
			v.Order = &o
		} else {
			err = failure("ORDER_NOT_FOUND", NotAdmitted, nil)
		}
	case BalancesQuery:
		v.Balances, _ = r.engine.Balances(q.AccountID)
	case BookQuery:
		v.Bids, v.Asks, err = r.engine.Book(q.Limit)
	case TradesQuery:
		v.ThroughSeq = v.AsOfSeq
		if q.ThroughSeq != nil {
			v.ThroughSeq = *q.ThroughSeq
		}
		v.Trades, err = r.engine.TradePage(q.AfterSeq, q.AfterIndex, v.ThroughSeq, q.Limit)
	case ResultQuery:
		key := d.Key{RunID: q.RunID, AccountID: q.AccountID, Kind: q.RequestKind, RequestID: q.ID}
		if result, found := r.engine.Result(key); found {
			v.Result = &result
			v.ResultState = "COMPLETED"
		} else if pending[key] != nil {
			v.ResultState = "PENDING"
		} else {
			v.ResultState = "UNKNOWN"
		}
	}
	if err == d.ErrInvalid {
		err = failure("INVALID_REQUEST", NotAdmitted, err)
	}
	req.reply <- queryReply{v, err}
}
