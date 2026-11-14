package stdlib

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"go.starlark.net/starlark"
)

func base64Call(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	safe, padding := false, true
	param := "data"
	if b.Name() == "base64.decode" {
		param = "text"
	}
	if err := unpack(b, args, kwargs, param, &value, "url_safe?", &safe, "padding?", &padding); err != nil {
		return nil, err
	}
	enc := base64.StdEncoding
	if safe {
		enc = base64.URLEncoding
	}
	if !padding {
		enc = enc.WithPadding(base64.NoPadding)
	}
	if b.Name() == "base64.encode" {
		s, err := bytesOf(value)
		if err != nil {
			return nil, err
		}
		if enc.EncodedLen(len(s)) > MaxBytes {
			return nil, errors.New("output exceeds byte limit")
		}
		return starlark.String(enc.EncodeToString([]byte(s))), nil
	}
	s, ok := starlark.AsString(value)
	if !ok {
		return nil, errors.New("expected Base64 text")
	}
	if err := text(s, MaxBytes); err != nil {
		return nil, err
	}
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("invalid Base64")
	}
	if enc.DecodedLen(len(s)) > MaxBytes {
		return nil, errors.New("output exceeds byte limit")
	}
	decoded, err := enc.Strict().DecodeString(s)
	if err != nil {
		return nil, errors.New("invalid Base64")
	}
	return starlark.Bytes(decoded), nil
}

func sha256Call(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	if err := unpack(b, args, kwargs, "data", &value); err != nil {
		return nil, err
	}
	s, err := bytesOf(value)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(s))
	return starlark.String(hex.EncodeToString(sum[:])), nil
}
