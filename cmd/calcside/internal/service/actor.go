// Package service holds the shared base for the domain services
// (cmd/calcside/internal/service/<domain>): the request actor and the typed error
// the api layer maps onto the wire error envelope.
package service

import (
	"encoding/json"

	"calcside/internal/types"
)

// Actor is the authenticated caller as the service layer sees it —
// derived from *auth.Principal by the api layer, never carrying the
// transport request itself.
type Actor struct {
	UserID string
	Email  string
	Kind   types.AuthKind
	// KeyID/KeySpec are set when Kind == api_key: the key that
	// authenticated and its stored instance spec (nil = full access).
	KeyID   string
	KeySpec json.RawMessage
	// KeyMaxInstances bounds live instances for a restricted key (0 = the
	// per-user limit only).
	KeyMaxInstances int
	// KeyViewOnly caps a key's ceiling at View.
	KeyViewOnly bool
	// WorkspaceID is the workspace this request acts in: a key's own
	// workspace, or the session workspace resolved per request
	// (personal by default).
	WorkspaceID string
}

// ViaKey reports whether the actor authenticated by API key.
func (a Actor) ViaKey() bool { return a.Kind == types.AuthAPIKey }

// Restricted reports whether the actor is an API key carrying an
// instance spec — it may only create and use instances from that spec.
func (a Actor) Restricted() bool { return len(a.KeySpec) > 0 }
