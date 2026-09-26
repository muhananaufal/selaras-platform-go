// Package authz asks OpenFGA who may read what (ADR-030).
//
// The relationship tuples are a projection of the clinic's consent ledger
// and membership; this package only reads and writes them. Every Check asks
// for HIGHER_CONSISTENCY: the go-sdk default is MINIMIZE_LATENCY, which may
// answer from a cache after a consent was revoked [go-sdk v0.8.3
// model_consistency_preference.go:20-33].
package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	fga "github.com/openfga/go-sdk"
	fgaclient "github.com/openfga/go-sdk/client"
)

// Tuple is one relationship: User has Relation to Object.
type Tuple struct {
	User     string
	Relation string
	Object   string
}

// Client checks and writes the tuples of one store and model.
type Client struct {
	fga     *fgaclient.OpenFgaClient
	storeID string
	modelID string
}

// StoreID and ModelID identify what this client talks to.
func (c *Client) StoreID() string { return c.storeID }
func (c *Client) ModelID() string { return c.modelID }

// Bootstrap finds the store named storeName, creating it when it does not
// exist, and makes model its latest authorization model.
//
// A model is written only when it differs from the latest one: OpenFGA keeps
// every version, and writing one on every start would bury the real changes
// under identical copies.
func Bootstrap(ctx context.Context, apiURL, storeName string, model []byte) (*Client, error) {
	var want fga.WriteAuthorizationModelRequest
	if err := json.Unmarshal(model, &want); err != nil {
		return nil, fmt.Errorf("reading the authorization model: %w", err)
	}

	c, err := fgaclient.NewSdkClient(&fgaclient.ClientConfiguration{ApiUrl: apiURL})
	if err != nil {
		return nil, fmt.Errorf("building the OpenFGA client: %w", err)
	}

	storeID, err := findOrCreateStore(ctx, c, storeName)
	if err != nil {
		return nil, err
	}
	if err := c.SetStoreId(storeID); err != nil {
		return nil, fmt.Errorf("selecting the store: %w", err)
	}

	modelID, err := ensureModel(ctx, c, want, model)
	if err != nil {
		return nil, err
	}
	if err := c.SetAuthorizationModelId(modelID); err != nil {
		return nil, fmt.Errorf("selecting the model: %w", err)
	}
	return &Client{fga: c, storeID: storeID, modelID: modelID}, nil
}

// Open connects to the store named storeName and its latest model, and
// writes nothing: the store and the model belong to clinic-svc, which
// bootstraps them. A store that does not exist is an error, not something to
// create - a reader that created it would answer every check with no.
func Open(ctx context.Context, apiURL, storeName string) (*Client, error) {
	c, err := fgaclient.NewSdkClient(&fgaclient.ClientConfiguration{ApiUrl: apiURL})
	if err != nil {
		return nil, fmt.Errorf("building the OpenFGA client: %w", err)
	}
	listed, err := c.ListStores(ctx).Options(fgaclient.ClientListStoresOptions{Name: &storeName}).Execute()
	if err != nil {
		return nil, fmt.Errorf("listing stores: %w", err)
	}
	storeID := ""
	for _, s := range listed.GetStores() {
		if s.GetName() == storeName {
			storeID = s.GetId()
		}
	}
	if storeID == "" {
		return nil, fmt.Errorf("no OpenFGA store named %q; clinic-svc bootstraps it", storeName)
	}
	if err := c.SetStoreId(storeID); err != nil {
		return nil, fmt.Errorf("selecting the store: %w", err)
	}
	latest, err := c.ReadLatestAuthorizationModel(ctx).Execute()
	if err != nil || latest.AuthorizationModel == nil {
		return nil, fmt.Errorf("reading the latest model of store %q: %w", storeName, err)
	}
	modelID := latest.AuthorizationModel.GetId()
	if err := c.SetAuthorizationModelId(modelID); err != nil {
		return nil, fmt.Errorf("selecting the model: %w", err)
	}
	return &Client{fga: c, storeID: storeID, modelID: modelID}, nil
}

func findOrCreateStore(ctx context.Context, c *fgaclient.OpenFgaClient, name string) (string, error) {
	listed, err := c.ListStores(ctx).Options(fgaclient.ClientListStoresOptions{Name: &name}).Execute()
	if err != nil {
		return "", fmt.Errorf("listing stores: %w", err)
	}
	for _, s := range listed.GetStores() {
		if s.GetName() == name {
			return s.GetId(), nil
		}
	}
	created, err := c.CreateStore(ctx).Body(fgaclient.ClientCreateStoreRequest{Name: name}).Execute()
	if err != nil {
		return "", fmt.Errorf("creating store %q: %w", name, err)
	}
	return created.GetId(), nil
}

func ensureModel(ctx context.Context, c *fgaclient.OpenFgaClient, want fga.WriteAuthorizationModelRequest, raw []byte) (string, error) {
	latest, err := c.ReadLatestAuthorizationModel(ctx).Execute()
	if err == nil && latest.AuthorizationModel != nil && sameModel(*latest.AuthorizationModel, raw) {
		return latest.AuthorizationModel.GetId(), nil
	}
	// An error here is not fatal on its own: a new store has no model yet,
	// and writing one is the answer. If the server is really unreachable the
	// write below fails and says so.
	written, err := c.WriteAuthorizationModel(ctx).Body(want).Execute()
	if err != nil {
		return "", fmt.Errorf("writing the authorization model: %w", err)
	}
	return written.GetAuthorizationModelId(), nil
}

// sameModel compares what a model means - its schema version, types and
// conditions - not its id (see sameJSON).
func sameModel(have fga.AuthorizationModel, want []byte) bool {
	stored, err := json.Marshal(fga.WriteAuthorizationModelRequest{
		SchemaVersion: have.SchemaVersion, TypeDefinitions: have.TypeDefinitions, Conditions: have.Conditions,
	})
	if err != nil {
		return false
	}
	same, err := sameJSON(stored, want)
	return err == nil && same
}

// Check answers whether user has relation to object, with higher
// consistency. An error is an error: the caller decides, and for a patient's
// data it refuses (fail closed, ADR-030).
func (c *Client) Check(ctx context.Context, user, relation, object string) (bool, error) {
	consistency := fga.CONSISTENCYPREFERENCE_HIGHER_CONSISTENCY
	resp, err := c.fga.Check(ctx).
		Body(fgaclient.ClientCheckRequest{User: user, Relation: relation, Object: object}).
		Options(fgaclient.ClientCheckOptions{Consistency: &consistency}).
		Execute()
	if err != nil {
		return false, fmt.Errorf("checking %s %s %s: %w", user, relation, object, err)
	}
	return resp.GetAllowed(), nil
}

// Write adds and removes tuples in one transaction. Writing a tuple that
// exists and deleting one that does not are both accepted: the projection
// that calls this is at-least-once.
func (c *Client) Write(ctx context.Context, writes, deletes []Tuple) error {
	if len(writes) == 0 && len(deletes) == 0 {
		return errors.New("nothing to write")
	}
	body := fgaclient.ClientWriteRequest{}
	for _, t := range writes {
		body.Writes = append(body.Writes, fgaclient.ClientTupleKey{User: t.User, Relation: t.Relation, Object: t.Object})
	}
	for _, t := range deletes {
		body.Deletes = append(body.Deletes, fgaclient.ClientTupleKeyWithoutCondition{User: t.User, Relation: t.Relation, Object: t.Object})
	}
	_, err := c.fga.Write(ctx).Body(body).Options(fgaclient.ClientWriteOptions{
		Conflict: fgaclient.ClientWriteConflictOptions{
			OnDuplicateWrites: fgaclient.CLIENT_WRITE_REQUEST_ON_DUPLICATE_WRITES_IGNORE,
			OnMissingDeletes:  fgaclient.CLIENT_WRITE_REQUEST_ON_MISSING_DELETES_IGNORE,
		},
	}).Execute()
	if err != nil {
		return fmt.Errorf("writing tuples: %w", err)
	}
	return nil
}

// Lazy is a checker for units that only read (ADR-030). It opens the store
// on the first check rather than at start, so a unit does not depend on
// clinic-svc having bootstrapped the store before it started; until then
// every check is an error, which callers answer with a refusal.
type Lazy struct {
	url, store string

	mu     sync.Mutex
	client *Client
}

// NewLazy returns a checker for the store named store at url.
func NewLazy(url, store string) *Lazy { return &Lazy{url: url, store: store} }

// Check opens the store if needed, then checks with higher consistency.
func (l *Lazy) Check(ctx context.Context, user, relation, object string) (bool, error) {
	c, err := l.open(ctx)
	if err != nil {
		return false, err
	}
	return c.Check(ctx, user, relation, object)
}

func (l *Lazy) open(ctx context.Context) (*Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.client != nil {
		return l.client, nil
	}
	c, err := Open(ctx, l.url, l.store)
	if err != nil {
		return nil, err
	}
	l.client = c
	return c, nil
}
