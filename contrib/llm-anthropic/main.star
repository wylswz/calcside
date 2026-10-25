# Anthropic Messages provider extension. Config comes from capability.yaml.


def _err(r):
    body = r.get("body", "")
    if len(body) > 800:
        body = body[:800]
    fail("llm provider: status " + str(r["status"]) + ": " + body)


def _auth():
    return {"x-api-key": config["api_key"], "anthropic-version": "2023-06-01"}


def _split_system(messages):
    system = ""
    rest = []
    for m in messages:
        if m["role"] == "system":
            if system != "":
                system += "\n"
            system += m["content"]
        else:
            rest.append(m)
    return system, rest


def _post(body):
    r = net.post(url=config["base_url"] + "/v1/messages",
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


def respond(messages, model, options=None):
    system, rest = _split_system(messages)
    body = {"model": model, "max_tokens": config["max_tokens"], "messages": rest}
    if system != "":
        body["system"] = system
    if options != None:
        for k in options:
            body[k] = options[k]
    d = _post(body)
    text = ""
    for b in d["content"]:
        if b["type"] == "text":
            text += b["text"]
    u = d.get("usage", {})
    return {
        "text": text,
        "finish_reason": d.get("stop_reason", ""),
        "usage": {
            "input_tokens": u.get("input_tokens", 0),
            "output_tokens": u.get("output_tokens", 0),
        },
    }


def structured(messages, schema, model):
    system, rest = _split_system(messages)
    body = {
        "model": model,
        "max_tokens": config["max_tokens"],
        "messages": rest,
        "tools": [{"name": "emit", "description": "Emit the answer",
                   "input_schema": schema}],
        "tool_choice": {"type": "tool", "name": "emit"},
    }
    if system != "":
        body["system"] = system
    d = _post(body)
    for b in d["content"]:
        if b["type"] == "tool_use" and b["name"] == "emit":
            v = b["input"]
            _check_schema(v, schema)
            return v
    fail("llm provider: no tool_use block in response")
