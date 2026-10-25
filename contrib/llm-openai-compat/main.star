# OpenAI Chat Completions provider extension (also the base for
# openai-compatible servers). Config is validated from capability.yaml.


def _err(r):
    body = r.get("body", "")
    if len(body) > 800:
        body = body[:800]
    fail("llm provider: status " + str(r["status"]) + ": " + body)


def _options(options):
    out = {}
    if options != None:
        for k in options:
            if k == "max_output_tokens":
                out["max_tokens"] = options[k]
            else:
                out[k] = options[k]
    return out


def _auth():
    key = config["api_key"]
    if key == "":
        return {}
    return {"Authorization": "Bearer " + key}


def _chat(messages, model, extra=None):
    body = {"model": model, "messages": messages}
    if extra != None:
        for k in extra:
            body[k] = extra[k]
    r = net.post(url=config["base_url"] + "/chat/completions",
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


def _decode(text):
    t = text.strip()
    if t.startswith("```"):
        lines = t.split("\n")
        if len(lines) > 2 and lines[-1].strip() == "```":
            t = "\n".join(lines[1:-1])
    return json.decode(t.strip())


def respond(messages, model, options=None):
    d = _chat(messages, model, _options(options))
    c = d["choices"][0]
    u = d.get("usage", {})
    return {
        "text": c["message"].get("content", ""),
        "finish_reason": c.get("finish_reason", ""),
        "usage": {
            "input_tokens": u.get("prompt_tokens", 0),
            "output_tokens": u.get("completion_tokens", 0),
        },
    }


def structured(messages, schema, model):
    if config["json_schema"]:
        d = _chat(messages, model, {"response_format": {
            "type": "json_schema",
            "json_schema": {"name": "output", "schema": schema},
        }})
        v = _decode(d["choices"][0]["message"]["content"])
    else:
        fixed = [x for x in messages]
        fixed.append({"role": "user", "content":
            "Respond with ONLY a JSON value conforming to this schema, " +
            "no prose, no code fences: " + json.encode(schema)})
        d = _chat(fixed, model)
        v = _decode(d["choices"][0]["message"]["content"])
    _check_schema(v, schema)
    return v
