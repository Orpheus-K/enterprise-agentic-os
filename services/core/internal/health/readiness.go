package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type Transport string

const (
	Starting     Transport = "starting"
	Connected    Transport = "connected"
	Reconnecting Transport = "reconnecting"
	Disconnected Transport = "disconnected"
	Listening    Transport = "listening"
	Stopped      Transport = "stopped"
)

type Readiness struct {
	mu        sync.RWMutex
	transport Transport
	worker    bool
	probe     func(context.Context) error
}
type Status struct {
	Ready     bool      `json:"ready"`
	Transport Transport `json:"transport"`
	Worker    bool      `json:"worker"`
	Storage   string    `json:"storage"`
	Model     string    `json:"model"`
}

func NewReadiness(probe func(context.Context) error) *Readiness {
	return &Readiness{transport: Starting, probe: probe}
}
func (r *Readiness) SetTransport(t Transport) { r.mu.Lock(); defer r.mu.Unlock(); r.transport = t }
func (r *Readiness) SetWorker(running bool)   { r.mu.Lock(); defer r.mu.Unlock(); r.worker = running }
func (r *Readiness) Snapshot(ctx context.Context) Status {
	r.mu.RLock()
	s := Status{Transport: r.transport, Worker: r.worker, Storage: "unavailable", Model: "not_probed"}
	r.mu.RUnlock()
	limited, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if r.probe(limited) == nil {
		s.Storage = "available"
	}
	// Read current transport state after the storage probe; a disconnect must not reuse an old ready snapshot.
	r.mu.RLock()
	s.Transport = r.transport
	s.Worker = r.worker
	r.mu.RUnlock()
	s.Ready = s.Storage == "available" && s.Worker && (s.Transport == Connected || s.Transport == Listening)
	return s
}
func (r *Readiness) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	ready := r.Snapshot(request.Context()).Ready
	if !ready {
		w.WriteHeader(503)
	}
	_ = json.NewEncoder(w).Encode(struct {
		Ready bool `json:"ready"`
	}{ready})
}
