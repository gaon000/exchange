package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	d "github.com/gaon000/exchange/internal/domain"
	"github.com/gaon000/exchange/internal/strictjson"
)

type savedRequest struct {
	Version int       `json:"version"`
	URL     string    `json:"url"`
	RunID   string    `json:"runId"`
	Account string    `json:"account"`
	Kind    d.Kind    `json:"kind"`
	Place   *d.Place  `json:"place,omitempty"`
	Cancel  *d.Cancel `json:"cancel,omitempty"`
}

func validateContext(endpoint, run, account string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("invalid --url")
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, portErr := strconv.Atoi(port)
	ip := net.ParseIP(host)
	if err != nil || portErr != nil || n < 1 || n > 65535 || ip == nil || !ip.IsLoopback() || u.Scheme != "http" || u.User != nil || u.Opaque != "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("--url must be numeric loopback HTTP with a port and no credentials/path/query/fragment")
	}
	if !d.ValidRunID(run) || !d.ValidID(account) {
		return errors.New("valid --run-id and --account are required")
	}
	return nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file too large")
	}
	return b, nil
}

func loadServerReport(o *options) error {
	b, err := readLimited(o.report, 1<<20)
	if err != nil {
		return err
	}
	var r struct{ Event, RunID, Listen string }
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	if r.Event != "ready" {
		return errors.New("server report is not ready")
	}
	o.endpoint, o.runID = "http://"+r.Listen, r.RunID
	return nil
}

func newRequest(o options) (savedRequest, error) {
	r := savedRequest{Version: 1, URL: strings.TrimSuffix(o.endpoint, "/"), RunID: o.runID, Account: o.account}
	if o.command == "place" {
		price, err := decimalUnits(o.price, 2)
		if err != nil {
			return r, fmt.Errorf("price: %w", err)
		}
		qty, err := decimalUnits(o.quantity, 4)
		if err != nil {
			return r, fmt.Errorf("quantity: %w", err)
		}
		r.Kind = d.PlaceOrder
		r.Place = &d.Place{ClientOrderID: o.id, Symbol: d.Symbol, Side: d.Side(o.side), Type: "LIMIT", TimeInForce: "GTC", PriceTicks: price, QuantityLots: qty}
	} else {
		r.Kind = d.CancelOrder
		r.Cancel = &d.Cancel{CancelRequestID: o.id, ClientOrderID: o.orderID}
	}
	return r, r.validate()
}

func (r savedRequest) validate() error {
	if r.Version != 1 {
		return errors.New("unsupported request file version")
	}
	if err := validateContext(r.URL, r.RunID, r.Account); err != nil {
		return err
	}
	c := d.Command{RunID: r.RunID, AccountID: r.Account, Kind: r.Kind}
	switch {
	case r.Kind == d.PlaceOrder && r.Place != nil && r.Cancel == nil:
		c.Place = *r.Place
	case r.Kind == d.CancelOrder && r.Cancel != nil && r.Place == nil:
		c.Cancel = *r.Cancel
	default:
		return errors.New("request must contain exactly its active payload")
	}
	return c.Validate()
}

func (r savedRequest) paths() (post, lookup, id string) {
	if r.Kind == d.PlaceOrder {
		return "/v1/orders", "/v1/results/place/" + r.Place.ClientOrderID, r.Place.ClientOrderID
	}
	return "/v1/cancellations", "/v1/results/cancel/" + r.Cancel.CancelRequestID, r.Cancel.CancelRequestID
}

func (r savedRequest) body() []byte {
	var body any = r.Place
	if r.Kind == d.CancelOrder {
		body = r.Cancel
	}
	b, _ := json.Marshal(body) // validate has restricted all values to supported domain scalars.
	return b
}

func saveRequest(path string, r savedRequest) (string, error) {
	var f *os.File
	var err error
	if path == "" {
		parent := filepath.Join("var", "requests")
		if err = os.MkdirAll(parent, 0700); err != nil {
			return "", err
		}
		f, err = os.CreateTemp(parent, "request-*.json")
	} else {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	}
	if err != nil {
		return "", err
	}
	path = f.Name()
	writeErr := json.NewEncoder(f).Encode(r)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return path, fmt.Errorf("request not sent; could not save %s: %w", path, err)
	}
	return path, nil
}

func loadRequest(path string) (savedRequest, error) {
	var r savedRequest
	b, err := readLimited(path, 8192)
	if err != nil {
		return r, err
	}
	if err := strictjson.Decode(b, &r); err != nil {
		return r, err
	}
	return r, r.validate()
}
