// Package httpx provides the HTTP pieces shared by every unit.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
)

const statusKey = "status"

// Health separates two questions that are often mixed up: is this process
// alive, and is it ready to take traffic. Kubernetes uses them for
// different things - a failing liveness triggers a restart, a failing
// readiness only takes the pod out of the service.
type Health struct {
	ready atomic.Bool
}

func NewHealth() *Health { return &Health{} }

// SetReady is called once the dependencies are ready, not when the process
// starts.
func (h *Health) SetReady(ready bool) { h.ready.Store(ready) }

func (h *Health) Live(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{statusKey: "alive"})
}

func (h *Health) Ready(w http.ResponseWriter, _ *http.Request) {
	if !h.ready.Load() {
		WriteJSON(w, http.StatusServiceUnavailable, map[string]string{statusKey: "not ready"})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{statusKey: "ready"})
}

// WriteJSON writes body as JSON. An encode error cannot be recovered from
// because the status and headers are already sent, but it is logged -
// swallowing it silently is a defect found in the legacy system (finding
// B8).
func WriteJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to encode response body", "error", err, "status", code)
	}
}
