// Package service holds the shared base for the domain services
// (internal/service/<domain>): the request actor and the typed error
// the api layer maps onto the wire error envelope.
package service

import "calcside/internal/types"

// Actor is the authenticated caller as the service layer sees it —
// derived from *auth.Principal by the api layer, never carrying the
// transport request itself.
type Actor struct {
	UserID string
	Email  string
	Kind   types.AuthKind
}

// ViaKey reports whether the actor authenticated by API key.
func (a Actor) ViaKey() bool { return a.Kind == types.AuthAPIKey }
