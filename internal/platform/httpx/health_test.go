package httpx_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/httpx"
)

func TestHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ready      bool
		probe      string
		wantStatus int
		wantBody   string
	}{
		{name: "live probe answers before dependencies are up", ready: false, probe: "live", wantStatus: http.StatusOK, wantBody: "alive"},
		{name: "live probe still answers once ready", ready: true, probe: "live", wantStatus: http.StatusOK, wantBody: "alive"},
		{name: "ready probe refuses traffic before dependencies are up", ready: false, probe: "ready", wantStatus: http.StatusServiceUnavailable, wantBody: "not ready"},
		{name: "ready probe accepts traffic once dependencies are up", ready: true, probe: "ready", wantStatus: http.StatusOK, wantBody: "ready"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := httpx.NewHealth()
			h.SetReady(tt.ready)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)

			if tt.probe == "live" {
				h.Live(rec, req)
			} else {
				h.Ready(rec, req)
			}

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body["status"] != tt.wantBody {
				t.Errorf("status field = %q, want %q", body["status"], tt.wantBody)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}

// Liveness dan readiness harus terpisah: proses yang hidup tetapi belum
// siap tidak boleh di-restart, ia hanya perlu dikeluarkan dari service.
func TestLivenessIsIndependentOfReadiness(t *testing.T) {
	t.Parallel()

	h := httpx.NewHealth()
	h.SetReady(false)

	rec := httptest.NewRecorder()
	h.Live(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("liveness returned %d while not ready; a restart loop would follow", rec.Code)
	}
}
