// Package httpx menyediakan potongan HTTP yang dipakai bersama seluruh unit.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
)

const statusKey = "status"

// Health memisahkan dua pertanyaan yang sering dicampur: apakah proses ini
// hidup, dan apakah ia siap menerima trafik. Kubernetes memakai keduanya
// untuk hal berbeda - liveness yang gagal memicu restart, readiness yang
// gagal hanya mengeluarkan pod dari service.
type Health struct {
	ready atomic.Bool
}

func NewHealth() *Health { return &Health{} }

// SetReady dipanggil setelah dependensi siap, bukan saat proses menyala.
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

// WriteJSON menulis body sebagai JSON. Galat encode tidak bisa diperbaiki
// karena status dan header sudah terkirim, tetapi ia dicatat - menelannya
// diam-diam adalah cacat yang ditemukan di sistem lama (temuan B8).
func WriteJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("failed to encode response body", "error", err, "status", code)
	}
}
