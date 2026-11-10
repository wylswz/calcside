# OpenAI provider extension using the Responses API. Model ids and
# defaults live in capability.yaml; requests go through the instance net
# capability with secret-injected Authorization headers.


def _err(r):
    body = r.get("body", "")
    if len(body) > 800:
        body = body[:800]
    fail("llm provider: status " + str(r["status"]) + ": " + body)


def _auth():
    key = config["api_key"]
    if key == "":
        return {}
    return {"Authorization": "Bearer " + key}


def _options(options):
    out = {}
    if options != None:
        for k in options:
            if k == "max_tokens":
                out["max_output_tokens"] = options[k]
            else:
                out[k] = options[k]
    return out


def _post(body):
    r = net.post(url=config["base_url"] + "/responses",
                 body=json.encode(body), headers=_auth(),
                 content_type="application/json")
    if r["status"] < 200 or r["status"] >= 300:
        _err(r)
    return json.decode(r["body"])


def _check_schema(v, schema):
    t = schema.get("type")
    if t == "object" and type(v) != "dict":
        fail("structured output: expected object")
    elif t == "array" and type(v) != "list":
        fail("structured output: expected array")
    elif t == "string" and type(v) != "string":
        fail("structured output: expected string")
    elif t == "number" and type(v) not in ["int", "float"]:
        fail("structured output: expected number")
    elif t == "integer" and type(v) != "int":
        fail("structured output: expected integer")
    elif t == "boolean" and type(v) != "bool":
        fail("structured output: expected boolean")
    if t == "object":
        for req in schema.get("required", []):
            if req not in v:
                fail("structured output: missing required key " + req)


def _text(d):
    text = ""
    for item in d.get("output", []):
        if item.get("type") != "message":
            continue
        for c in item.get("content", []):
            if c.get("type") == "output_text":
                text += c.get("text", "")
    return text


def _decode(text):
    t = text.strip()
    if t.startswith("```"):
        lines = t.split("\n")
        if len(lines) > 2 and lines[-1].strip() == "```":
            t = "\n".join(lines[1:-1])
    return json.decode(t.strip())


def _usage(d):
    u = d.get("usage", {})
    return {
        "input_tokens": u.get("input_tokens", 0),
        "output_tokens": u.get("output_tokens", 0),
    }


def respond(messages, model, options=None):
    body = {"model": model, "input": messages}
    opts = _options(options)
    for k in opts:
        body[k] = opts[k]
    d = _post(body)
    return {
        "text": _text(d),
        "finish_reason": d.get("status", ""),
        "usage": _usage(d),
    }


def structured(messages, schema, model):
    if config["json_schema"]:
        body = {
            "model": model,
            "input": messages,
            "text": {"format": {
                "type": "json_schema",
                "name": "output",
                "schema": schema,
                "strict": False,
            }},
        }
        d = _post(body)
        v = _decode(_text(d))
    else:
        fixed = [x for x in messages]
        fixed.append({"role": "user", "content":
            "Respond with ONLY a JSON value conforming to this schema, " +
            "no prose, no code fences: " + json.encode(schema)})
        d = _post({"model": model, "input": fixed})
        v = _decode(_text(d))
    _check_schema(v, schema)
    return v


_streams = stream


def stream(messages, model, options=None):
    body = {"model": model, "input": messages}
    opts = _options(options)
    debug_errors = opts.pop("debug_errors", False)
    if type(debug_errors) != "bool":
        fail("llm provider: debug_errors must be a bool")
    for k in opts:
        if k != "stream":
            body[k] = opts[k]
    body["stream"] = True  # streaming cannot be disabled via options
    hdrs = dict(_auth())
    hdrs["Content-Type"] = "application/json"
    resp = net.stream("POST", config["base_url"] + "/responses",
                      body=json.encode(body), headers=hdrs)
    state = {"done": False}

    _CODES = ["server_error", "rate_limit_exceeded", "insufficient_quota",
              "invalid_prompt", "invalid_request_error", "model_not_found",
              "context_length_exceeded", "unsupported_parameter",
              "unsupported_value", "vector_store_timeout"]
    _REASONS = ["max_output_tokens", "content_filter"]

    def _code(v):
        # fixed whitelist only — provider codes are never echoed
        return type(v) == "string" and v in _CODES and v

    def _reason(v):
        return type(v) == "string" and v in _REASONS and v

    def _detail(v):
        # opt-in bounded diagnostic: only when debug_errors=True, string
        # only. The decoded message is redacted AGAIN first (decoded
        # text can reconstruct secrets the raw-byte redactor missed),
        # then capped at 512 code points and JSON-escaped.
        if not debug_errors or type(v) != "string" or v == "":
            return ""
        v = _redact_error(v)
        parts = []
        trunc = False
        for ch in v.codepoints():
            if len(parts) == 512:
                trunc = True
                break
            parts.append(ch)
        msg = "".join(parts)
        if trunc:
            msg += "... [truncated]"
        return ": " + json.encode(msg)

    def _stream_failure(d):
        # terminal provider failure: default fails with a fixed label +
        # whitelisted code/reason; debug_errors opts in to a bounded,
        # redacted, escaped excerpt of the provider message only.
        t = d.get("type", "")
        r = d.get("response")
        r = r if type(r) == "dict" else {}
        if t == "response.failed":
            e = r.get("error")
            e = e if type(e) == "dict" else {}
            c = _code(e.get("code"))
            fail("llm provider: response.failed" +
                 (" (code=" + c + ")" if c else "") +
                 _detail(e.get("message")))
        elif t == "response.incomplete":
            det = r.get("incomplete_details")
            det = det if type(det) == "dict" else {}
            rs = _reason(det.get("reason"))
            e = r.get("error")
            e = e if type(e) == "dict" else {}
            fail("llm provider: response.incomplete" +
                 (" (reason=" + rs + ")" if rs else "") +
                 _detail(e.get("message")))
        else:
            c = _code(d.get("code"))
            if not c:
                e = d.get("error")
                e = e if type(e) == "dict" else {}
                c = _code(e.get("code"))
            msg = d.get("message")
            if type(msg) != "string" or msg == "":
                e = d.get("error")
                e = e if type(e) == "dict" else {}
                msg = e.get("message")
            fail("llm provider: error" +
                 (" (code=" + c + ")" if c else "") +
                 _detail(msg))

    def _http_error(rsp):
        # Non-2xx: default is the fixed status error with NO body pull.
        # Under debug_errors, collect a bounded 16KiB JSON error body
        # and select only error.message — re-redacted + truncated by
        # _detail. Anything else stays bare status; raw body is never
        # echoed, headers/code are not selected.
        if not debug_errors:
            rsp["body"].close()
            fail("llm provider: status " + str(rsp["status"]))
        buf = b""
        too_large = False
        while True:
            has, chunk = rsp["body"].next()
            if not has:
                break
            if len(buf) + len(chunk) > 16384:
                too_large = True
                break
            buf += chunk
        rsp["body"].close()
        if too_large:
            fail("llm provider: status " + str(rsp["status"]))
        dec = json.decode(str(buf), default=None)
        msg = None
        if type(dec) == "dict":
            e = dec.get("error")
            if type(e) == "dict" and type(e.get("message")) == "string":
                msg = e["message"]
        fail("llm provider: status " + str(rsp["status"]) +
             ("" if msg == None else _detail(msg)))

    if resp["status"] < 200 or resp["status"] >= 300:
        _http_error(resp)
    ct = resp["headers"].get("Content-Type") or ""
    if ct.split(";", 1)[0].strip().lower() != "text/event-stream":
        resp["body"].close()
        fail("llm provider: unexpected response content type")
    events = _streams.sse(resp["body"])

    def nxt():
        if state["done"]:
            return (False, None)
        while True:
            has, ev = events.next()
            if not has:
                fail("llm provider: stream ended before completion")
            d = json.decode(ev["data"])
            t = d.get("type", "")
            if t == "response.output_text.delta":
                return (True, {"type": "text_delta", "text": d.get("delta", "")})
            if t == "response.completed":
                r = d.get("response", {})
                if r.get("status") != "completed":
                    fail("llm provider: response did not complete")
                u = r.get("usage")
                usage = None
                if type(u) == "dict":
                    usage = {"input_tokens": u.get("input_tokens"),
                             "output_tokens": u.get("output_tokens")}
                state["done"] = True
                events.close()
                return (True, {"type": "done", "finish_reason": "completed",
                               "usage": usage})
            if t in ["response.output_item.added", "response.output_item.done"]:
                if d.get("item", {}).get("type") == "function_call":
                    fail("llm provider: tool calls are not supported in streaming")
            elif t in ["response.failed", "response.incomplete", "error"]:
                _stream_failure(d)

    return _streams.new(nxt)
