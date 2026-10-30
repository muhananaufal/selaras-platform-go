package authz_test

import (
	"strings"
	"testing"

	"github.com/muhananaufal/selaras-platform-go/internal/platform/authz"
)

func TestServerFromEnv(t *testing.T) {
	cases := []struct {
		name, url, token string
		want             authz.Server
		wantErr          string
	}{
		// No OpenFGA at all is a valid configuration: every clinician read is
		// refused and the unit says so at start (ADR-030).
		{name: "neither", want: authz.Server{}},
		{name: "both", url: "http://openfga:8080", token: "k", want: authz.Server{URL: "http://openfga:8080", Token: "k"}},
		// A URL without the key would start a unit whose every call is refused
		// by a keyed server - or, worse, accepted by one that lost its keys.
		// Refusing to start says which variable is missing (ADR-031).
		{name: "url without token", url: "http://openfga:8080", wantErr: "OPENFGA_API_TOKEN"},
		{name: "token without url", token: "k", wantErr: "OPENFGA_URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("OPENFGA_URL", c.url)
			t.Setenv("OPENFGA_API_TOKEN", c.token)
			got, err := authz.ServerFromEnv()
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("ServerFromEnv() error = %v; want one naming %s", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("ServerFromEnv() = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}
