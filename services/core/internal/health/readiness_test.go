package health

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestReadinessLifecycle(t *testing.T) {
	storage := true
	r := NewReadiness(func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("unbounded probe")
		}
		if !storage {
			return errors.New("private database secret")
		}
		return nil
	})
	for _, tc := range []struct {
		state         Transport
		worker, ready bool
	}{
		{Starting, false, false}, {Connected, false, false}, {Connected, true, true}, {Reconnecting, true, false}, {Disconnected, true, false}, {Connected, true, true}, {Listening, true, true}, {Stopped, false, false},
	} {
		r.SetTransport(tc.state)
		r.SetWorker(tc.worker)
		s := r.Snapshot(t.Context())
		if s.Ready != tc.ready || s.Model != "not_probed" {
			t.Fatalf("unexpected state %+v", s)
		}
	}
	r.SetTransport(Connected)
	r.SetWorker(true)
	storage = false
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/ready", nil))
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"ready":false}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("readiness leaked details or reported ready")
	}
	storage = true
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/ready", nil))
	if w.Code != 200 {
		t.Fatal("did not recover")
	}
}
func TestDisconnectDuringProbe(t *testing.T) {
	var r *Readiness
	r = NewReadiness(func(context.Context) error { r.SetTransport(Disconnected); return nil })
	r.SetWorker(true)
	r.SetTransport(Connected)
	if r.Snapshot(t.Context()).Ready {
		t.Fatal("stale connected snapshot")
	}
}
func TestConcurrentLifecycle(t *testing.T) {
	r := NewReadiness(func(context.Context) error { return nil })
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			r.SetWorker(true)
			r.SetTransport(Connected)
			_ = r.Snapshot(t.Context())
			r.SetTransport(Disconnected)
			r.SetWorker(false)
		})
	}
	wg.Wait()
	r.SetTransport(Stopped)
	r.SetWorker(false)
	if r.Snapshot(t.Context()).Ready {
		t.Fatal("stopped ready")
	}
}
