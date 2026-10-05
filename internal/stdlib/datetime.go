package stdlib

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"go.starlark.net/starlark"
)

var rfc3339Pattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func datetimeCall(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	if strings.HasPrefix(b.Name(), "datetime.parse_") {
		var s string
		if err := unpack(b, args, kwargs, "text", &s); err != nil {
			return nil, err
		}
		if err := text(s, 128); err != nil {
			return nil, err
		}
		layout := time.RFC3339Nano
		if b.Name() == "datetime.parse_date" {
			layout = time.DateOnly
		}
		if layout == time.RFC3339Nano && !rfc3339Pattern.MatchString(s) {
			return nil, errors.New("invalid RFC3339 timestamp")
		}
		stamp, err := time.ParseInLocation(layout, s, time.UTC)
		if err != nil || stamp.Year() < 1 || stamp.Year() > 9999 {
			return nil, errors.New("invalid explicit date or timestamp")
		}
		if layout == time.DateOnly && stamp.Format(layout) != s {
			return nil, errors.New("expected YYYY-MM-DD")
		}
		_, offset := stamp.Zone()
		if offset <= -24*3600 || offset >= 24*3600 {
			return nil, errors.New("invalid UTC offset")
		}
		millis := stamp.UnixMilli()
		if millis < -62135596800000 || millis > 253402300799999 {
			return nil, errors.New("timestamp out of range")
		}
		return starlark.MakeInt64(millis), nil
	}
	var millis int64
	if err := unpack(b, args, kwargs, "timestamp_ms", &millis); err != nil {
		return nil, err
	}
	if millis < -62135596800000 || millis > 253402300799999 {
		return nil, errors.New("timestamp out of range")
	}
	stamp := time.UnixMilli(millis).UTC()
	layout := time.RFC3339Nano
	if b.Name() == "datetime.format_date" {
		layout = time.DateOnly
	}
	return starlark.String(stamp.Format(layout)), nil
}
