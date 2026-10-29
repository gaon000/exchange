package api

import (
	"io"
	"net/http/httptest"
	"testing"
)

type failingMetricWriter struct {
	*httptest.ResponseRecorder
	flushOnly bool
}

func (w failingMetricWriter) Write(p []byte) (int, error) {
	if w.flushOnly {
		return w.ResponseRecorder.Write(p)
	}
	return 2, io.ErrClosedPipe
}
func (w failingMetricWriter) FlushError() error { return io.ErrClosedPipe }

func TestResponseMetricsDoNotTreatWriteAsClientReceipt(t *testing.T) {
	for _, flushOnly := range []bool{false, true} {
		s, _ := newStub(t, DefaultOptions())
		w := failingMetricWriter{httptest.NewRecorder(), flushOnly}
		s.ServeHTTP(w, request("GET", "/health/live", ""))
		m := s.Statistics()
		if m.Handler.Count != 1 || m.Encode.Count != 1 || m.Write.Count != 1 || m.Status2xx != 1 || m.WriteErrors != 1 || m.Bytes == 0 || m.Active != 0 {
			t.Fatalf("lost failed transmission: %+v", m)
		}
	}
	s, _ := newStub(t, DefaultOptions())
	w := httptest.NewRecorder()
	s.write(w, 200, make(chan int))
	m := s.Statistics()
	if m.EncodeErrors != 1 || m.Status5xx != 1 || m.Status2xx != 0 || m.WriteErrors != 0 {
		t.Fatal(m)
	}
}
