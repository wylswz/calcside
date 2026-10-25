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
