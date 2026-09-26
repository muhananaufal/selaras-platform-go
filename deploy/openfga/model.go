// Package openfga embeds the clinic access model (ADR-030) that clinic-svc
// writes to OpenFGA at start. model.json is generated from model.fga by the
// fga CLI; test.sh fails when the two drift apart.
package openfga

import _ "embed"

// Model is the authorization model as OpenFGA's API takes it.
//
//go:embed model.json
var Model []byte
