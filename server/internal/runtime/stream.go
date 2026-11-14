package runtime

import (
	"context"
	"encoding/json"
)

type InvokeStreamSink interface {
	Start() error
	Item(json.RawMessage) error
}

type InvokeStreamSinkStats interface {
	InvokeStreamSink
	Stats() (started bool, items int64, bytes int64)
}

type invokeStreamKey struct{}

func WithInvokeStream(ctx context.Context, sink InvokeStreamSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, invokeStreamKey{}, sink)
}

func InvokeStreamFromContext(ctx context.Context) InvokeStreamSink {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(invokeStreamKey{}).(InvokeStreamSink)
	return s
}
