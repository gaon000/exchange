package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/gaon000/exchange/internal/strictjson"
)

type httpClient struct {
	base, runID string
	client      *http.Client
	trace       *json.Encoder
}

type exchangeRecord struct {
	Method   string `json:"method"`
	Path     string `json:"path"`
	Account  string `json:"account"`
	Request  string `json:"request"`
	Response string `json:"response"`
	Status   int    `json:"status"`
	Error    string `json:"error,omitempty"`
}

func newHTTPClient(base, runID string, trace io.Writer) *httpClient {
	return &httpClient{base: base, runID: runID, trace: json.NewEncoder(trace), client: &http.Client{
		Timeout:       5 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (c *httpClient) request(ctx context.Context, account, method, path string, body any) ([]byte, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Run-ID", c.runID)
	req.Header.Set("X-Account-ID", account)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	record := exchangeRecord{Method: method, Path: path, Account: account, Request: string(payload)}
	resp, requestErr := c.client.Do(req)
	var raw []byte
	if resp != nil {
		record.Status = resp.StatusCode
		var readErr error
		raw, readErr = io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		requestErr = errors.Join(requestErr, readErr, resp.Body.Close())
		if len(raw) > 1<<20 {
			requestErr = errors.Join(requestErr, errors.New("response exceeded 1 MiB"))
		}
		record.Response = string(raw)
		if resp.StatusCode != http.StatusOK {
			requestErr = errors.Join(requestErr, fmt.Errorf("HTTP %d", resp.StatusCode))
		}
	}
	if requestErr != nil {
		record.Error = requestErr.Error()
	}
	if err := errors.Join(requestErr, c.trace.Encode(record)); err != nil {
		return raw, fmt.Errorf("%s %s (%s): %w", method, path, account, err)
	}
	return raw, nil
}

func expect[T any](ctx context.Context, c *httpClient, account, method, path string, body any, want T) ([]byte, error) {
	raw, err := c.request(ctx, account, method, path, body)
	if err != nil {
		return raw, err
	}
	var got T
	if err := strictjson.Decode(raw, &got); err != nil {
		return raw, fmt.Errorf("%s: invalid response: %w", path, err)
	}
	if !reflect.DeepEqual(got, want) {
		wanted, _ := json.Marshal(want)
		return raw, fmt.Errorf("%s (%s): expected %s, got %s", path, account, wanted, raw)
	}
	return raw, nil
}
