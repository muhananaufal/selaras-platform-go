package handler

// State names shared by several endpoints.
//
// All three appear in assessment personalisation, the coaching curriculum, and
// the graduation report. Naming them once keeps the three the same: a client
// handling "pending" from one endpoint and "in_progress" from another has to
// write two branches for one state.
const (
	statusNotRequested = "not_requested"
	statusPending      = "pending"
	statusCompleted    = "completed"
	statusReady        = "ready"
	statusFailed       = "failed"

	// statusUnknown is used for an unrecognised enum value. It is NOT a real
	// state - it is a marker that the data is outside what this code knows, and
	// a client must not treat it as "running".
	statusUnknown = "unknown"
)

// The role names of a message sender.
//
// The three endpoints that display conversations - general chat, coaching
// threads, and authentication naming the account role - use the same words.
// Naming them once keeps the three the same.
const (
	roleNameUser  = "user"
	roleNameModel = "model"
)
