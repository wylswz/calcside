// Package remote is the HTTP/JSON transport for the runtime contract.
// It adds nothing to the contract itself: requests and responses are
// the serializable types in internal/runtime, sent as JSON bodies over
// mutually authenticated POSTs to the owning node.
//
// Authentication is a shared-secret HMAC over method, path, timestamp,
// and body. It authenticates the *caller node*, not the end user — user
// auth already happened on the API tier and its result travels inside
// the request as Owner.
package remote

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"calcside/internal/runtime"
)

const (
	headerNode = "X-Calcside-Node"
	headerTime = "X-Calcside-Time"
	headerSig  = "X-Calcside-Signature"

	// maxClockSkew bounds how old a signed request may be.
	maxClockSkew = 60 * time.Second
)

// sign computes the request signature for an outbound call.
func sign(key []byte, method, path string, ts time.Time, body []byte) string {
	sum := sha256.Sum256(body)
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s\n%s\n%d\n", method, path, ts.Unix())
	mac.Write(sum[:])
	return hex.EncodeToString(mac.Sum(nil))
}

// SignRequest sets the auth headers on an outbound request. Exported so
// the worker's auxiliary calls to the API tier (ext resolution) use the
// same scheme as runtime calls.
func SignRequest(key []byte, callerNode string, r *http.Request, body []byte, ts time.Time) {
	r.Header.Set(headerNode, callerNode)
	r.Header.Set(headerTime, fmt.Sprint(ts.Unix()))
	r.Header.Set(headerSig, sign(key, r.Method, r.URL.RequestURI(), ts, body))
}

// VerifyRequest checks an inbound request's signature headers.
func VerifyRequest(key []byte, r *http.Request, body []byte, now time.Time) error {
	ts, err := strconv.ParseInt(r.Header.Get(headerTime), 10, 64)
	if err != nil {
		return errors.New("remote: bad timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > maxClockSkew || d < -maxClockSkew {
		return errors.New("remote: request timestamp out of window")
	}
	want := sign(key, r.Method, r.URL.RequestURI(), time.Unix(ts, 0), body)
	got := r.Header.Get(headerSig)
	if !hmac.Equal([]byte(want), []byte(got)) {
		return errors.New("remote: bad signature")
	}
	return nil
}

// Error kinds are carried across the wire as stable strings so the API
// tier can keep switching on errors.Is sentinels.
func kindOf(err error) string {
	switch {
	case errors.Is(err, runtime.ErrNotFound):
		return "not_found"
	case errors.Is(err, runtime.ErrNotRunning):
		return "not_running"
	case errors.Is(err, runtime.ErrTooMany):
		return "too_many"
	case errors.Is(err, runtime.ErrBadCapability):
		return "bad_capability"
	case errors.Is(err, runtime.ErrNoCapability):
		return "no_capability"
	case errors.Is(err, runtime.ErrNotOwner):
		return "not_owner"
	case errors.Is(err, runtime.ErrBadSpec):
		return "bad_spec"
	case errors.Is(err, runtime.ErrNoSuchPath):
		return "no_such_path"
	case errors.Is(err, runtime.ErrFS):
		return "fs_error"
	case errors.Is(err, runtime.ErrStaleEpoch):
		return "stale_epoch"
	default:
		return "internal"
	}
}

func errorOf(kind, msg string) error {
	var sentinel error
	switch kind {
	case "not_found":
		sentinel = runtime.ErrNotFound
	case "not_running":
		sentinel = runtime.ErrNotRunning
	case "too_many":
		sentinel = runtime.ErrTooMany
	case "bad_capability":
		sentinel = runtime.ErrBadCapability
	case "no_capability":
		sentinel = runtime.ErrNoCapability
	case "not_owner":
		sentinel = runtime.ErrNotOwner
	case "bad_spec":
		sentinel = runtime.ErrBadSpec
	case "no_such_path":
		sentinel = runtime.ErrNoSuchPath
	case "fs_error":
		sentinel = runtime.ErrFS
	case "stale_epoch":
		sentinel = runtime.ErrStaleEpoch
	default:
		sentinel = runtime.Errf(errInternal, "%s", "")
	}
	return &runtime.Error{Kind: sentinel, Msg: msg}
}

var errInternal = errors.New("runtime: internal error")

// statusOf maps an error kind onto an HTTP status for the wire. The
// status is transport plumbing; the kind string is the contract.
func statusOf(err error) int {
	switch kindOf(err) {
	case "not_found", "no_such_path":
		return http.StatusNotFound
	case "not_owner":
		return http.StatusForbidden
	case "too_many":
		return http.StatusConflict
	case "bad_spec", "bad_capability", "no_capability", "not_running":
		return http.StatusBadRequest
	case "stale_epoch":
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}
