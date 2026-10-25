# Gemini generateContent provider extension. Config comes from capability.yaml.


def _err(r):
    body = r.get("body", "")
    if len(body) > 800:
        body = body[:800]
    fail("llm provider: status " + str(r["status"]) + ": " + body)


def _split(messages):
    system = ""
    contents = []
    for m in messages:
        if m["role"] == "system":
            if system != "":
                system += "\n"
            system += m["content"]
        elif m["role"] == "assistant":
            contents.append({"role": "model", "parts": [{"text": m["content"]}]})
        else:
            contents.append({"role": "user", "parts": [{"text": m["content"]}]})
    return system, contents


def _post(model, body):
    u = (config["base_url"] + "/v1beta/models/" + url.path_escape(model) +
         ":generateContent")
    r = net.post(url=u, body=json.encode(body),
                 headers={"x-goog-api-key": config["api_key"]},
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
    for p in d["candidates"][0]["content"].get("parts", []):
        text += p.get("text", "")
    return text


def respond(messages, model, options=None):
    system, contents = _split(messages)
    body = {"contents": contents}
    if system != "":
        body["systemInstruction"] = {"parts": [{"text": system}]}
    if options != None:
        body["generationConfig"] = options
    d = _post(model, body)
    u = d.get("usageMetadata", {})
    return {
        "text": _text(d),
        "finish_reason": d["candidates"][0].get("finishReason", ""),
        "usage": {
            "input_tokens": u.get("promptTokenCount", 0),
            "output_tokens": u.get("candidatesTokenCount", 0),
        },
    }


def structured(messages, schema, model):
    system, contents = _split(messages)
    body = {
        "contents": contents,
        "generationConfig": {
            "responseMimeType": "application/json",
            "responseSchema": schema,
        },
    }
    if system != "":
        body["systemInstruction"] = {"parts": [{"text": system}]}
    d = _post(model, body)
    v = json.decode(_text(d))
    _check_schema(v, schema)
    return v
