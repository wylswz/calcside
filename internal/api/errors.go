package api

import (
	"errors"
	"net/http"

	"calcside/internal/api/dto"
	"calcside/internal/service"
	"calcside/internal/types"
)

func errEnv(code types.APIErrorCode, msg string) any {
	return dto.ErrorEnvelope{Error: dto.APIError{Code: code, Message: msg}}
}

// statusByCode maps wire error codes to HTTP status; codes absent from
// the table are 400.
var statusByCode = map[types.APIErrorCode]int{
	types.ErrCodeUnauthorized:    http.StatusUnauthorized,
	types.ErrCodeForbidden:       http.StatusForbidden,
	types.ErrCodeCSRF:            http.StatusForbidden,
	types.ErrCodeNotFound:        http.StatusNotFound,
	types.ErrCodeConflict:        http.StatusConflict,
	types.ErrCodeNotRunning:      http.StatusConflict,
	types.ErrCodeTooLarge:        http.StatusRequestEntityTooLarge,
	types.ErrCodeTooMany:         http.StatusTooManyRequests,
	types.ErrCodeInternal:        http.StatusInternalServerError,
	types.ErrCodeSecretsDisabled: http.StatusServiceUnavailable,
}

func httpStatus(code types.APIErrorCode) int {
	if s, ok := statusByCode[code]; ok {
		return s
	}
	return http.StatusBadRequest
}

// fail renders an error as a rawJSON response: *service.Error keeps its
// code/message; anything else is a 500 internal exposing err.Error().
func fail(err error) rawJSON {
	var se *service.Error
	if errors.As(err, &se) {
		return rawJSON{httpStatus(se.Code), errEnv(se.Code, se.Msg)}
	}
	return rawJSON{http.StatusInternalServerError, errEnv(types.ErrCodeInternal, err.Error())}
}
