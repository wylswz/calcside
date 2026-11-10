# Pull-based streams (EP-0021)

`stream` is an engine module (not a capability): pull-based, ordered,
one-shot iterables scoped to a single execution. When an application's
`main` returns a stream on a live `invoke`/`test` call, the API serves
that response as `text/event-stream` (`item` events carrying raw
per-item JSON, then exactly one `done` or `error` terminal event). A
stream returned by a later interrupt handler has no original HTTP
connection: it is still consumed, validated and counted — statistics
land in run history, payloads are never retained or replayed.

```python
s = stream.new(next_fn)                    # callback () -> (has_value, value)
s = stream.from_list(list_or_tuple)        # lazy cursor over the value
events = stream.sse(bytes_stream)          # -> {event, data, id}
mapped = s.map(fn)                          # lazy, claims the source cursor
total = s.reduce(fn, initial)               # one result after full fold
flat = s.flatmap(fn)                        # fn -> stream | list | tuple
r = net.stream("POST", url, body=..., headers=...)
body = r["body"]                            # Stream[bytes]
for chunk in body:
    ...
has, v = body.next()                        # (False, None) at EOF
body.close()                                # idempotent
resp = ext.llm.stream(provider, messages, model=None, options=None)
```

## Semantics

- One active cursor per stream; iteration consumes values. `break` /
  `return` / unwind only RELEASES the cursor — the same stream stays
  open and may resume via `.next()` or a later `for` in the same exec.
  Early stop is the caller's explicit `stream.close()`. Aliases share
  the cursor — no replay, no concurrent iteration.
- Every stream registers in the owner execution scope at creation, is
  torn down deterministically before the gate disarms (never-consumed
  AND never-closed handles included — exec end is the safety net, not
  early close), and cannot be read in a later exec or another thread —
  stale access faults the current execution.
- Streams are opaque: no len/index/hash/JSON, `repr` never pulls, and
  the engine `Freeze` is a no-op. At most 128 live streams per exec —
  every stage of a gated pipeline (native source, wrapper, derived
  stream) counts separately.

## net.stream

Same security machinery as `net.request` (allow_hosts, method rules,
DNS pinning, private-network policy, secret host/redirect/header rules).
Headers open eagerly inside the gate; the body is demand-read in
bounded chunks (≤32 KiB raw, ≤64 KiB yielded), redacted incrementally
before bytes reach scripts. `max_response_bytes` bounds BOTH the raw
bytes read and the redacted output (fail-closed on marker expansion);
`timeout_ms` covers the open plus every pull with no idle reset.
Every pull and the final close go through the gate with
`stream_action=open|pull|close` and `stream_id`; hook denials cannot
skip native cleanup.

`stream.sse` bounds each source chunk to 64 KiB and each framed
line/event to 1 MiB; framing errors are sticky stream failures — a
sticky error aborts every later pull and the whole exec, while EOF is a
normal `(False, None)` result. `ext.llm.stream` may deliver `done`
with `usage: None` when the provider does not report counts.

## ext.llm.stream

Optional provider protocol `stream(messages, model, options=None)`.
v1 events: `{type:"text_delta",text}` and exactly one terminal
`{type:"done",finish_reason,usage}`; usage is `None` or
`{input_tokens,output_tokens}` with reported fields only. Providers
without `stream` fail with `unsupported_stream` before any network
call. Early break yields no synthetic `done`.

`stream.new` binds only the callback — values captured by the closure
are NOT owned or closed by the stream. To release a resource early,
close it explicitly: `body.close()` on a `net.stream` body, or
`events.close()` inside your own callback. `stream.sse` is the one
exception: its `.close()` does cascade because the byte stream is its
explicit input. There is no callback-supplied upstream/close hook —
callback streams can only promise exec-end cleanup, so an early
`ext.llm.stream` break leaves the underlying HTTP bounded by the exec
scope, not cancelled mid-flight.

### OpenAI stream: opt-in error diagnostics

`ext.llm.stream("openai", [{"role": "user", "content": "halo"}],
model="gpt-6-luna", options={"debug_errors": True})` adds a bounded,
JSON-escaped excerpt of the provider's `error.message` to stream
failure errors (first 512 Unicode chars, then `... [truncated]`).

It is an adapter-local option (removed before the request body is sent,
never an OpenAI parameter), **defaults off**, and applies only to
`contrib/llm-openai` streaming — `respond`/`structured` and other
providers are unaffected. Only the `message` field is opted in;
whitelisted code/reason suppression is unchanged. The SSE body is still
secret-redacted incrementally before decoding, so known secrets are
stripped — but upstream may copy your prompts/data into its message:
**audit and exec errors can persist that content**. Turn it off after
diagnosis.

Decrypted/decoded upstream messages are re-redacted before the excerpt
is taken (JSON-escaped or otherwise encoded secrets cannot bypass it),
then capped and escaped. The warning above still applies.

The same `debug_errors` opt-in applies to non-2xx HTTP responses: the
adapter reads at most 16 KiB of the JSON error body and selects only
`error.message`; anything malformed/oversized stays a bare status
error, and the option is never sent upstream.


## Application output streaming

`POST /api/v1/applications/{id}/test` and `/invoke` answer
`text/event-stream` automatically when `main` returns a stream — no
Accept negotiation. Frames: `event: item` + `data: <exact redacted item
JSON>` per validated item, then one terminal `event: done` or
`event: error` carrying `data: <ApplicationRunResponse>` with
`result.streaming`, `stream_items` and `stream_bytes`. Per-item
redaction and output-schema validation happen before delivery; a
failing item ends the stream with the prefix already sent. When the
application has no configured output schema the output contract
defaults to `{}` (any bounded JSON value), so mixed-type and scalar
streams are valid — an explicit schema still constrains every item
independently, and applications published before this default changed
keep their pinned contract until republished. Hard caps:
1 MiB per item (existing JSON limits), 16 MiB and 100,000 items total.
Between nodes the same pull flows over the signed worker protocol as
bounded NDJSON frames (`{"type":"start"|"item"|"result"|"error"}`).
`interrupt.wait` inside a streamed result is a runtime failure, never a
waiting run. Run history stores the counters only — streamed payloads
are not retained.

If the caller disconnects mid-stream the invocation is cancelled and
the run finalizes as `unknown` — delivery counters captured at the API
still persist, but a terminal audit frame lost with the transport is
not recoverable.

```python
def main(input):
    rows = stream.from_list([[1, 2], [3]])
    return rows.flatmap(lambda row: row).map(lambda n: {"n": n})

def total(input):
    return stream.from_list([1, 2, 3]).reduce(lambda a, b: a + b, 0)
    # reduce yields one item — 6 — or the initial value when the source is
    # empty; no output schema is needed for scalar streams.
```

```console
$ curl -N -X POST .../api/v1/applications/app_x/invoke -d '{}'
event: item
data: {"n":1}

event: item
data: {"n":2}

event: item
data: {"n":3}

event: done
data: {"result":{"id":"inv_...","streaming":true,"stream_items":3,"stream_bytes":21},"run_id":"run_...","run_status":"succeeded"}
```

Item redaction and schema validation apply to each item as an
independent JSON value — a secret spanning two items is never
reassembled. `net.stream` byte-level cumulative redaction is unchanged
and unrelated to this framing.
