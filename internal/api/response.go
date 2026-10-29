package api

import (
	"encoding/json"
	"net/http"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
)

// OrderView is a current order at one applied command boundary.
type OrderView struct {
	RunID   string   `json:"runId"`
	AsOfSeq d.Int    `json:"asOfSeq"`
	Order   *d.Order `json:"order"`
}

// BaseBalance expresses BTC in lots.
type BaseBalance struct {
	AvailableLots d.Int `json:"availableLots"`
	ReservedLots  d.Int `json:"reservedLots"`
}

// QuoteBalance expresses USDT in atoms.
type QuoteBalance struct {
	AvailableAtoms d.Int `json:"availableAtoms"`
	ReservedAtoms  d.Int `json:"reservedAtoms"`
}

// BalancesView contains only the requesting account's balances.
type BalancesView struct {
	RunID   string       `json:"runId"`
	AsOfSeq d.Int        `json:"asOfSeq"`
	BTC     BaseBalance  `json:"BTC"`
	USDT    QuoteBalance `json:"USDT"`
}

// Level is a public aggregate price/quantity pair without account/order IDs.
type Level struct {
	PriceTicks   d.Int `json:"priceTicks"`
	QuantityLots d.Int `json:"quantityLots"`
}

// BookView contains bounded price-sorted levels on both sides.
type BookView struct {
	RunID   string  `json:"runId"`
	AsOfSeq d.Int   `json:"asOfSeq"`
	Bids    []Level `json:"bids"`
	Asks    []Level `json:"asks"`
}

// Trade is public market data; counterparty identities are intentionally absent.
type Trade struct {
	TradeID      string `json:"tradeId"`
	PriceTicks   d.Int  `json:"priceTicks"`
	QuantityLots d.Int  `json:"quantityLots"`
	TakerSide    d.Side `json:"takerSide"`
}

// TradeCursor identifies the last returned trade for the next fixed-window page.
type TradeCursor struct {
	AfterSeq   d.Int `json:"afterSeq"`
	AfterIndex d.Int `json:"afterIndex"`
}

// TradesView preserves the first page's ThroughSeq even when new trades arrive.
// A nonnil NextCursor may lead to an empty final page when the limit was exact.
type TradesView struct {
	RunID      string       `json:"runId"`
	AsOfSeq    d.Int        `json:"asOfSeq"`
	ThroughSeq d.Int        `json:"throughSeq"`
	Trades     []Trade      `json:"trades"`
	NextCursor *TradeCursor `json:"nextCursor"`
}

// ErrorResponse is an attempt outcome, never a cached business rejection.
type ErrorResponse struct {
	Error rt.Failure `json:"error"`
}
type stateResponse struct {
	State string `json:"state"`
}

func encode(value any) ([]byte, error) { return json.Marshal(value) }

func (s *Server) fail(w http.ResponseWriter, code string, outcome rt.Outcome) {
	status := http.StatusServiceUnavailable
	switch code {
	case "INVALID_REQUEST":
		status = http.StatusBadRequest
	case "UNKNOWN_ACCOUNT", "HOST_FORBIDDEN", "ORIGIN_FORBIDDEN":
		status = http.StatusForbidden
	case "RUN_MISMATCH", "IDEMPOTENCY_CONFLICT", "RUN_LIMIT_REACHED":
		status = http.StatusConflict
	case "BODY_TOO_LARGE":
		status = http.StatusRequestEntityTooLarge
	case "UNSUPPORTED_MEDIA_TYPE":
		status = http.StatusUnsupportedMediaType
	case "QUEUE_FULL", "TOO_MANY_REQUESTS", "DUPLICATE_WAITERS_FULL":
		status = http.StatusTooManyRequests
	case "RESULT_TIMEOUT":
		status = http.StatusGatewayTimeout
	case "REQUEST_TIMEOUT":
		status = http.StatusRequestTimeout
	case "ORDER_NOT_FOUND", "ROUTE_NOT_FOUND":
		status = http.StatusNotFound
	case "METHOD_NOT_ALLOWED":
		status = http.StatusMethodNotAllowed
	case "PROTOCOL_UNSUPPORTED":
		status = http.StatusHTTPVersionNotSupported
	case "NOT_READY", "STORAGE_UNAVAILABLE", "INTERNAL_FAILURE":
	default:
		code, outcome = "INTERNAL_FAILURE", rt.Unknown
	}
	s.write(w, status, ErrorResponse{rt.Failure{Code: code, Outcome: outcome}})
}

func (s *Server) writeView(w http.ResponseWriter, q rt.Query, v rt.View) {
	switch q.Kind {
	case rt.OrderQuery:
		s.write(w, http.StatusOK, OrderView{v.RunID, v.AsOfSeq, v.Order})
	case rt.BalancesQuery:
		s.write(w, http.StatusOK, BalancesView{v.RunID, v.AsOfSeq, BaseBalance{v.Balances.Base.Available, v.Balances.Base.Reserved}, QuoteBalance{v.Balances.Quote.Available, v.Balances.Quote.Reserved}})
	case rt.BookQuery:
		b := BookView{v.RunID, v.AsOfSeq, make([]Level, 0, len(v.Bids)), make([]Level, 0, len(v.Asks))}
		for _, level := range v.Bids {
			b.Bids = append(b.Bids, Level{level.PriceTicks, level.QuantityLots})
		}
		for _, level := range v.Asks {
			b.Asks = append(b.Asks, Level{level.PriceTicks, level.QuantityLots})
		}
		s.write(w, http.StatusOK, b)
	case rt.TradesQuery:
		t := TradesView{RunID: v.RunID, AsOfSeq: v.AsOfSeq, ThroughSeq: v.ThroughSeq, Trades: make([]Trade, 0, len(v.Trades))}
		for _, trade := range v.Trades {
			t.Trades = append(t.Trades, Trade{trade.TradeID, trade.PriceTicks, trade.QuantityLots, trade.TakerSide})
		}
		if len(v.Trades) == q.Limit {
			last := v.Trades[len(v.Trades)-1]
			t.NextCursor = &TradeCursor{last.CommandSeq, last.MatchIndex}
		}
		s.write(w, http.StatusOK, t)
	case rt.ResultQuery:
		if v.Result != nil {
			s.write(w, http.StatusOK, v.Result)
		} else if v.ResultState == "PENDING" {
			s.write(w, http.StatusAccepted, stateResponse{"PENDING"})
		} else {
			s.write(w, http.StatusNotFound, stateResponse{"UNKNOWN"})
		}
	}
}
