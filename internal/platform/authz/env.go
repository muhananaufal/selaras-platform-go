package authz

import (
	"fmt"
	"os"
)

// ServerFromEnv reads OPENFGA_URL and OPENFGA_API_TOKEN. Neither set is a
// valid configuration - the unit runs without OpenFGA and refuses every
// clinician read (ADR-030). One without the other is refused, so a unit
// never starts sending unauthenticated calls to a server that holds the
// consent tuples (ADR-031).
func ServerFromEnv() (Server, error) {
	srv := Server{URL: os.Getenv("OPENFGA_URL"), Token: os.Getenv("OPENFGA_API_TOKEN")}
	switch {
	case srv.URL != "" && srv.Token == "":
		return Server{}, fmt.Errorf("OPENFGA_URL is set but OPENFGA_API_TOKEN is not: OpenFGA demands its preshared key")
	case srv.URL == "" && srv.Token != "":
		return Server{}, fmt.Errorf("OPENFGA_API_TOKEN is set but OPENFGA_URL is not")
	}
	return srv, nil
}
