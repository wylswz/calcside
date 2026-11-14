package service

import (
	"fmt"

	"calcside/internal/types"
)

// Error is a domain failure carrying the wire error code and message.
// The api layer maps Code onto an HTTP status; Msg is the exact wire
// message.
type Error struct {
	Code types.APIErrorCode
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }
func (e *Error) Unwrap() error { return e.Err }

// Errf builds an *Error with a formatted message.
func Errf(code types.APIErrorCode, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// NotFound builds a not_found error.
func NotFound(msg string) *Error {
	return &Error{Code: types.ErrCodeNotFound, Msg: msg}
}

// Conflict builds a conflict error.
func Conflict(msg string) *Error {
	return &Error{Code: types.ErrCodeConflict, Msg: msg}
}

// Forbidden builds a forbidden error.
func Forbidden(msg string) *Error {
	return &Error{Code: types.ErrCodeForbidden, Msg: msg}
}

// BadRequest builds a bad_request error.
func BadRequest(format string, args ...any) *Error {
	return &Error{Code: types.ErrCodeBadRequest, Msg: fmt.Sprintf(format, args...)}
}

// Internal wraps an unexpected error; the message is err.Error()
// (the API has always exposed internal error text on 500s).
func Internal(err error) *Error {
	return &Error{Code: types.ErrCodeInternal, Msg: err.Error(), Err: err}
}
