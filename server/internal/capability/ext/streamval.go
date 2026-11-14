package ext

import (
	"errors"
	"unicode/utf8"

	"go.starlark.net/starlark"

	"calcside/internal/stream"
)

const (
	maxStreamTextBytes = 1 << 20
	maxFinishBytes     = 256
)

// validateProviderStream wraps a provider's returned stream in a
// native validator so a custom provider cannot claim streaming success
// with a non-stream value or malformed events (EP-0021). v1 events are
// text only: {type:"text_delta",text} or exactly one terminal
// {type:"done",finish_reason,usage}. EOF before done fails; extra keys
// are stripped rather than copied into metadata.
func validateProviderStream(thread *starlark.Thread, provider, model string, v starlark.Value) (*stream.Stream, error) {
	src, ok := v.(*stream.Stream)
	if !ok {
		return nil, errors.New("provider stream must return a stream")
	}
	scope := stream.FromThread(thread)
	if scope == nil || src.Scope() != scope {
		return nil, stream.ErrOutOfScope
	}
	var doneSeen bool
	var usage map[string]any
	pull := func(t *starlark.Thread) (starlark.Value, bool, error) {
		if doneSeen {
			return starlark.None, false, nil
		}
		v, ok, err := src.Pull(t)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, errors.New("llm stream: ended without terminal done event")
		}
		ev, isDone, u, verr := validStreamEvent(v)
		if verr != nil {
			return nil, false, verr
		}
		if isDone {
			doneSeen = true
			usage = u
			if cerr := src.Close(); cerr != nil {
				return nil, false, cerr
			}
		}
		return ev, true, nil
	}
	w, err := stream.New(scope, pull, nil, src)
	if err != nil {
		return nil, err
	}
	w.SetTerminalMeta(func() map[string]any {
		m := map[string]any{"provider": provider, "model": model}
		if doneSeen {
			m["complete"] = true
			if usage != nil {
				m["usage"] = usage
			}
		}
		return m
	})
	return w, nil
}

// validStreamEvent validates and sanitizes one provider stream event.
// Unknown keys are stripped; only validated fields are emitted.
func validStreamEvent(v starlark.Value) (starlark.Value, bool, map[string]any, error) {
	d, ok := v.(*starlark.Dict)
	if !ok {
		return nil, false, nil, errors.New("llm stream: invalid event")
	}
	get := func(k string) (starlark.Value, bool) {
		v, found, err := d.Get(starlark.String(k))
		if err != nil || !found {
			return nil, false
		}
		return v, true
	}
	tv, ok := get("type")
	if !ok {
		return nil, false, nil, errors.New("llm stream: invalid event")
	}
	ts, ok := tv.(starlark.String)
	if !ok {
		return nil, false, nil, errors.New("llm stream: invalid event")
	}
	switch string(ts) {
	case "text_delta":
		xv, ok := get("text")
		if !ok {
			return nil, false, nil, errors.New("llm stream: invalid event")
		}
		xs, ok := xv.(starlark.String)
		if !ok || !utf8.ValidString(string(xs)) || len(xs) > maxStreamTextBytes {
			return nil, false, nil, errors.New("llm stream: invalid text_delta")
		}
		out := starlark.NewDict(2)
		_ = out.SetKey(starlark.String("type"), ts)
		_ = out.SetKey(starlark.String("text"), xs)
		return out, false, nil, nil
	case "done":
		fv, ok := get("finish_reason")
		if !ok {
			return nil, false, nil, errors.New("llm stream: invalid event")
		}
		fs, ok := fv.(starlark.String)
		if !ok || fs == "" || len(fs) > maxFinishBytes || !utf8.ValidString(string(fs)) {
			return nil, false, nil, errors.New("llm stream: invalid done event")
		}
		out := starlark.NewDict(3)
		_ = out.SetKey(starlark.String("type"), ts)
		_ = out.SetKey(starlark.String("finish_reason"), fs)
		var usage map[string]any
		if uv, found := get("usage"); found && uv != starlark.None {
			ud, ok := uv.(*starlark.Dict)
			if !ok || ud.Len() > 2 {
				return nil, false, nil, errors.New("llm stream: invalid usage")
			}
			usage = map[string]any{}
			su := starlark.NewDict(2)
			for _, item := range ud.Items() {
				k, _ := item[0].(starlark.String)
				ks := string(k)
				if ks != "input_tokens" && ks != "output_tokens" {
					return nil, false, nil, errors.New("llm stream: invalid usage")
				}
				val := item[1]
				if val == starlark.None {
					_ = su.SetKey(k, starlark.None)
					continue
				}
				iv, ok := val.(starlark.Int)
				if !ok {
					return nil, false, nil, errors.New("llm stream: invalid usage")
				}
				i64, ok := iv.Int64()
				if !ok || i64 < 0 {
					return nil, false, nil, errors.New("llm stream: invalid usage")
				}
				_ = su.SetKey(k, iv)
				usage[ks] = i64
			}
			_ = out.SetKey(starlark.String("usage"), su)
		} else {
			_ = out.SetKey(starlark.String("usage"), starlark.None)
		}
		return out, true, usage, nil
	}
	return nil, false, nil, errors.New("llm stream: unsupported event type")
}
