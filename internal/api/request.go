package api

import (
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	d "github.com/gaon000/exchange/internal/domain"
	rt "github.com/gaon000/exchange/internal/runtime"
	"github.com/gaon000/exchange/internal/strictjson"
)

func route(path string) (d.Kind, rt.Query, string) {
	q := rt.Query{}
	switch path {
	case "/v1/orders":
		return d.PlaceOrder, q, http.MethodPost
	case "/v1/cancellations":
		return d.CancelOrder, q, http.MethodPost
	case "/v1/balances":
		q.Kind = rt.BalancesQuery
	case "/v1/book":
		q.Kind, q.Limit = rt.BookQuery, 20
	case "/v1/trades":
		q.Kind, q.Limit = rt.TradesQuery, 100
	case "/health/live", "/health/ready":
	default:
		switch {
		case strings.HasPrefix(path, "/v1/orders/"):
			q.Kind, q.ID = rt.OrderQuery, strings.TrimPrefix(path, "/v1/orders/")
		case strings.HasPrefix(path, "/v1/results/place/"):
			q.Kind, q.RequestKind, q.ID = rt.ResultQuery, d.PlaceOrder, strings.TrimPrefix(path, "/v1/results/place/")
		case strings.HasPrefix(path, "/v1/results/cancel/"):
			q.Kind, q.RequestKind, q.ID = rt.ResultQuery, d.CancelOrder, strings.TrimPrefix(path, "/v1/results/cancel/")
		default:
			return "", q, ""
		}
	}
	return "", q, http.MethodGet
}

func parseQuery(raw string, q *rt.Query) (bool, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return false, err
	}
	for name, items := range values {
		if len(items) != 1 {
			return false, d.ErrInvalid
		}
		n, err := d.ParseInt(items[0])
		if err != nil {
			return false, err
		}
		switch {
		case name == "limit" && (q.Kind == rt.BookQuery || q.Kind == rt.TradesQuery):
			max := d.Int(100)
			if q.Kind == rt.TradesQuery {
				max = 1000
			}
			if n < 1 || n > max {
				return false, d.ErrInvalid
			}
			q.Limit = int(n)
		case name == "throughSeq" && q.Kind == rt.TradesQuery:
			q.ThroughSeq = &n
		case name == "afterSeq" && q.Kind == rt.TradesQuery:
			q.AfterSeq = n
		case name == "afterIndex" && q.Kind == rt.TradesQuery:
			q.AfterIndex = n
		default:
			return false, d.ErrInvalid
		}
	}
	_, seq := values["afterSeq"]
	_, index := values["afterIndex"]
	if seq != index || seq && (q.ThroughSeq == nil || q.AfterSeq == 0 || q.AfterIndex == 0 || q.AfterSeq > *q.ThroughSeq) {
		return false, d.ErrInvalid
	}
	return true, nil
}

func decodeCommand(w http.ResponseWriter, r *http.Request, kind d.Kind, run, account string) (d.Command, string) {
	c := d.Command{RunID: run, AccountID: account, Kind: kind}
	contentType, ok := singleHeader(r, "Content-Type")
	media, params, err := mime.ParseMediaType(contentType)
	if !ok || err != nil || media != "application/json" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
		return c, "UNSUPPORTED_MEDIA_TYPE"
	}
	encoding := r.Header.Values("Content-Encoding")
	if len(encoding) > 1 || len(encoding) == 1 && !strings.EqualFold(encoding[0], "identity") {
		return c, "UNSUPPORTED_MEDIA_TYPE"
	}
	if r.ContentLength > 4096 {
		w.Header().Set("Connection", "close")
		return c, "BODY_TOO_LARGE"
	}
	body := http.MaxBytesReader(w, r.Body, 4096)
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		var size *http.MaxBytesError
		var timeout net.Error
		switch {
		case errors.As(err, &size):
			return c, "BODY_TOO_LARGE"
		case errors.As(err, &timeout) && timeout.Timeout():
			return c, "REQUEST_TIMEOUT"
		default:
			return c, "INVALID_REQUEST"
		}
	}
	if kind == d.PlaceOrder {
		err = strictjson.Decode(data, &c.Place)
	} else {
		err = strictjson.Decode(data, &c.Cancel)
	}
	if err != nil || c.Validate() != nil {
		return c, "INVALID_REQUEST"
	}
	return c, ""
}
