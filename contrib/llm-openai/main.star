load("llm_judge.star", "score_impl", "choose_impl", "rank_impl")


def _auth():
    return {"Authorization": "Bearer " + config["api_key"]}


def _chat(messages, model, extra=None):
    body = {"model": model, "messages": messages}
    if extra != None:
        for k in extra:
            body[k] = extra[k]
    r = net.post(url=config["base_url"] + "/chat/completions",
                 body=json.encode(body), headers=_auth(),
                 content_type="application/json")
    if r["status"] < 200 or r["status"] >= 300:
        fail("llm provider: status " + str(r["status"]))
    return json.decode(r["body"])


def _model(model):
    if model == None:
        return config["model"]
    return model


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


def respond(messages, model=None, options=None):
    opts = {}
    if options != None:
        for k in options:
            opts[k] = options[k]
    d = _chat(messages, _model(model), opts)
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


def _structured_once(messages, schema, model):
    if config["json_schema"]:
        d = _chat(messages, model, {"response_format": {
            "type": "json_schema",
            "json_schema": {"name": "output", "schema": schema, "strict": False},
        }})
        return d["choices"][0]["message"]["content"]
    schema_text = json.encode(schema)
    fixed = [m for m in messages]
    fixed.append({"role": "user", "content":
        "Respond with ONLY a JSON value conforming to this schema, no prose: " + schema_text})
    d = _chat(fixed, model)
    return d["choices"][0]["message"]["content"]


def structured(messages, schema, model=None):
    m = _model(model)
    text = _structured_once(messages, schema, m)
    v = json.decode(text)
    _check_schema(v, schema)
    return v


def score(content, criteria, scale=1.0, model=None):
    return score_impl(structured, content, criteria, scale, model)


def choose(instruction, options, model=None):
    return choose_impl(structured, instruction, options, model)


def rank(instruction, items, model=None):
    return rank_impl(structured, instruction, items, model)
