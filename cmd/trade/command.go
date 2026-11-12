package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	d "github.com/gaon000/exchange/internal/domain"
)

const usage = `Usage: trade <command> [flags]
Commands: place, cancel, balances, book, trades, order, result, retry
Use trade <command> -h for flags. Prices are USDT/BTC; quantities are BTC.
place/cancel save a new request file before sending. result/retry --request FILE
reuse that exact run/account/request; retries never generate a new request ID.
`

type options struct {
	command, endpoint, runID, account, report, request string
	id, orderID, side, price, quantity, kind           string
	limit                                              int
	through, afterSeq, afterIndex                      string
	timeout                                            time.Duration
}

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, err := io.WriteString(stderr, usage)
		return o, errors.Join(flag.ErrHelp, err)
	}
	o.command = args[0]
	f := flag.NewFlagSet("trade "+o.command, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.DurationVar(&o.timeout, "timeout", 5*time.Second, "per HTTP request deadline (greater than 0, at most 1m)")
	switch o.command {
	case "place", "cancel", "balances", "book", "trades", "order", "result":
		f.StringVar(&o.endpoint, "url", "http://127.0.0.1:8080", "numeric loopback HTTP base URL")
		f.StringVar(&o.runID, "run-id", "", "run UUID from server readiness report")
		f.StringVar(&o.account, "account", "", "registered test account")
		f.StringVar(&o.report, "server-report", "", "read URL/run ID from a server ready JSON file")
	case "retry":
	default:
		return o, fmt.Errorf("unknown command %q; %s", o.command, usage)
	}
	switch o.command {
	case "place", "cancel", "result", "retry":
		f.StringVar(&o.request, "request", "", "request file (new for place/cancel; existing for result/retry)")
	}
	switch o.command {
	case "place":
		f.StringVar(&o.id, "id", "", "required client order ID, reused on retry")
		f.StringVar(&o.side, "side", "", "BUY or SELL")
		f.StringVar(&o.price, "price", "", "USDT/BTC, at most 2 decimal places")
		f.StringVar(&o.quantity, "quantity", "", "BTC, at most 4 decimal places")
	case "cancel":
		f.StringVar(&o.id, "id", "", "required cancellation request ID, reused on retry")
		f.StringVar(&o.orderID, "order-id", "", "target client order ID")
	case "order":
		f.StringVar(&o.id, "id", "", "client order ID")
	case "result":
		f.StringVar(&o.kind, "kind", "", "place or cancel (without --request)")
		f.StringVar(&o.id, "id", "", "original request ID (without --request)")
	case "book":
		f.IntVar(&o.limit, "limit", 20, "levels per side, 1..100")
	case "trades":
		f.IntVar(&o.limit, "limit", 100, "trades per page, 1..1000")
		f.StringVar(&o.through, "through-seq", "", "fixed throughSeq from first page")
		f.StringVar(&o.afterSeq, "after-seq", "", "nextCursor.afterSeq")
		f.StringVar(&o.afterIndex, "after-index", "", "nextCursor.afterIndex")
	}
	if err := f.Parse(args[1:]); err != nil {
		return o, err
	}
	if f.NArg() != 0 || o.timeout <= 0 || o.timeout > time.Minute {
		return o, errors.New("invalid arguments or timeout")
	}
	if o.command == "retry" || o.command == "result" && o.request != "" {
		if o.request == "" {
			return o, errors.New("--request is required")
		}
		var conflict bool
		f.Visit(func(v *flag.Flag) {
			if v.Name != "request" && v.Name != "timeout" {
				conflict = true
			}
		})
		if conflict {
			return o, errors.New("saved request context cannot be overridden")
		}
		return o, nil
	}
	if o.report != "" {
		var conflict bool
		f.Visit(func(v *flag.Flag) {
			if v.Name == "url" || v.Name == "run-id" {
				conflict = true
			}
		})
		if conflict {
			return o, errors.New("--server-report cannot be combined with --url/--run-id")
		}
		if err := loadServerReport(&o); err != nil {
			return o, err
		}
	}
	if err := validateContext(o.endpoint, o.runID, o.account); err != nil {
		return o, err
	}
	if o.command == "order" && !d.ValidID(o.id) {
		return o, errors.New("invalid --id")
	}
	if o.command == "result" && (!d.ValidID(o.id) || o.kind != "place" && o.kind != "cancel") {
		return o, errors.New("result requires --kind place|cancel and --id, or --request")
	}
	if o.command == "book" && (o.limit < 1 || o.limit > 100) || o.command == "trades" && (o.limit < 1 || o.limit > 1000) {
		return o, errors.New("limit out of range")
	}
	if o.command == "trades" {
		if (o.afterSeq == "") != (o.afterIndex == "") || o.afterSeq != "" && o.through == "" {
			return o, errors.New("cursor requires --through-seq, --after-seq and --after-index")
		}
		for _, v := range []string{o.through, o.afterSeq, o.afterIndex} {
			if v == "" {
				continue
			}
			_, err := d.ParseInt(v)
			if err != nil {
				return o, errors.New("invalid trade cursor")
			}
		}
		// Cursor components must be positive even when equal to throughSeq.
		for _, v := range []string{o.afterSeq, o.afterIndex} {
			if v != "" {
				n, _ := d.ParseInt(v)
				if n == 0 {
					return o, errors.New("cursor must be positive")
				}
			}
		}
	}
	return o, nil
}

// decimalUnits scales exact decimal digits without floating point or rounding.
func decimalUnits(s string, decimals int) (d.Int, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, errors.New("invalid decimal")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) == 0 || len(fraction) > decimals {
			return 0, errors.New("unsupported decimal precision")
		}
	}
	return d.ParseInt(parts[0] + fraction + strings.Repeat("0", decimals-len(fraction)))
}

func (o options) queryPath() string {
	switch o.command {
	case "order":
		return "/v1/orders/" + o.id
	case "result":
		return "/v1/results/" + o.kind + "/" + o.id
	case "book":
		return "/v1/book?limit=" + strconv.Itoa(o.limit)
	case "trades":
		q := url.Values{"limit": {strconv.Itoa(o.limit)}}
		for k, v := range map[string]string{"throughSeq": o.through, "afterSeq": o.afterSeq, "afterIndex": o.afterIndex} {
			if v != "" {
				q.Set(k, v)
			}
		}
		return "/v1/trades?" + q.Encode()
	default:
		return "/v1/balances"
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	o, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if o.command == "retry" || o.command == "result" && o.request != "" {
		r, err := loadRequest(o.request)
		if err != nil {
			return err
		}
		return runSaved(ctx, o, r, stdout, stderr)
	}
	if o.command == "place" || o.command == "cancel" {
		r, err := newRequest(o)
		if err != nil {
			return err
		}
		path, err := saveRequest(o.request, r)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(stderr, "request saved: %s\nUse result --request or retry --request with this file; never change the ID after a timeout.\n", path); err != nil {
			return err
		}
		return sendMutation(ctx, o.timeout, r, stdout)
	}
	return query(ctx, o.timeout, o.endpoint, o.runID, o.account, o.queryPath(), o.command, stdout)
}
