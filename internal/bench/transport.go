package bench

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
)

// FailureStage identifies the first observed failing operation, not admission.
type FailureStage string

const (
	FailureRequestBuild   FailureStage = "request_build"
	FailureConnect        FailureStage = "connect"
	FailureRequestWrite   FailureStage = "request_write"
	FailureResponseHeader FailureStage = "response_headers"
	FailureResponseRead   FailureStage = "response_read"
	FailureResponseClose  FailureStage = "response_close"
	FailureRoundTrip      FailureStage = "round_trip"
)

// TransportErrorType is bounded diagnostic metadata; no raw error text is saved.
type TransportErrorType string

const (
	ErrorDeadlineExceeded   TransportErrorType = "deadline_exceeded"
	ErrorCanceled           TransportErrorType = "canceled"
	ErrorTimeout            TransportErrorType = "timeout"
	ErrorConnectionRefused  TransportErrorType = "connection_refused"
	ErrorConnectionReset    TransportErrorType = "connection_reset"
	ErrorBrokenPipe         TransportErrorType = "broken_pipe"
	ErrorUnexpectedEOF      TransportErrorType = "unexpected_eof"
	ErrorEOF                TransportErrorType = "eof"
	ErrorClosed             TransportErrorType = "closed"
	ErrorNetworkUnreachable TransportErrorType = "network_unreachable"
	ErrorOther              TransportErrorType = "other"
)

func transportErrorType(err error) TransportErrorType {
	for _, item := range []struct {
		err  error
		kind TransportErrorType
	}{
		{context.DeadlineExceeded, ErrorDeadlineExceeded}, {context.Canceled, ErrorCanceled},
		{syscall.ECONNREFUSED, ErrorConnectionRefused}, {syscall.ECONNRESET, ErrorConnectionReset},
		{syscall.EPIPE, ErrorBrokenPipe}, {io.ErrUnexpectedEOF, ErrorUnexpectedEOF}, {io.EOF, ErrorEOF},
		{net.ErrClosed, ErrorClosed}, {syscall.ENETUNREACH, ErrorNetworkUnreachable}, {syscall.EHOSTUNREACH, ErrorNetworkUnreachable},
	} {
		if errors.Is(err, item.err) {
			return item.kind
		}
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return ErrorTimeout
	}
	return ErrorOther
}

func validateFailure(o Outcome) error {
	// Historical v1 samples did not record these fields. Their absence is not
	// evidence of a successful transport or known admission.
	if o.FailureStage == "" && o.ErrorType == "" {
		return nil
	}
	if o.Class != TransportFailure && o.Class != TransportTimeout {
		return errors.New("transport metadata on non-transport outcome")
	}
	switch o.FailureStage {
	case FailureRequestBuild, FailureConnect, FailureRequestWrite, FailureResponseHeader, FailureResponseRead, FailureResponseClose, FailureRoundTrip:
	default:
		return errors.New("unknown transport failure stage")
	}
	switch o.ErrorType {
	case ErrorDeadlineExceeded, ErrorCanceled, ErrorTimeout, ErrorConnectionRefused, ErrorConnectionReset, ErrorBrokenPipe, ErrorUnexpectedEOF, ErrorEOF, ErrorClosed, ErrorNetworkUnreachable, ErrorOther:
	default:
		return errors.New("unknown transport error type")
	}
	return nil
}
