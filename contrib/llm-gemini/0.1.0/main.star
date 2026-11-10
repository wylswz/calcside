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


_streams = stream


def stream(messages, model, options=None):
    system, contents = _split(messages)
    body = {"contents": contents}
    if system != "":
        body["systemInstruction"] = {"parts": [{"text": system}]}
    if options != None:
        gc = {}
        for k in options:
            if k != "stream":  # the route selects streaming; stream is not a generationConfig field
                gc[k] = options[k]
        if len(gc) > 0:
            body["generationConfig"] = gc
    u = (config["base_url"] + "/v1beta/models/" + url.path_escape(model) +
         ":streamGenerateContent?alt=sse")
    resp = net.stream("POST", u, body=json.encode(body),
                      headers={"x-goog-api-key": config["api_key"],
                               "Content-Type": "application/json"})
    if resp["status"] < 200 or resp["status"] >= 300:
        resp["body"].close()
        fail("llm provider: status " + str(resp["status"]))
    ct = resp["headers"].get("Content-Type") or ""
    if ct.split(";", 1)[0].strip().lower() != "text/event-stream":
        resp["body"].close()
        fail("llm provider: unexpected response content type")
    events = _streams.sse(resp["body"])
    state = {"done": False, "pending": None, "usage": None}

    def nxt():
        if state["done"]:
            return (False, None)
        if state["pending"] != None:
            p = state["pending"]
            state["pending"] = None
            state["done"] = True
            events.close()
            return (True, p)
        while True:
            has, ev = events.next()
            if not has:
                fail("llm provider: stream ended without finishReason")
            d = json.decode(ev["data"])
            um = d.get("usageMetadata")
            if type(um) == "dict":
                state["usage"] = {"input_tokens": um.get("promptTokenCount"),
                                  "output_tokens": um.get("candidatesTokenCount")}
            udict = state["usage"]
            text = ""
            finish = ""
            cands = d.get("candidates", [])
            if len(cands) > 1:
                fail("llm provider: multi-candidate responses are not supported in streaming")
            for c in cands:
                for p in c.get("content", {}).get("parts", []):
                    if p.get("functionCall") != None:
                        fail("llm provider: function calls are not supported in streaming")
                    if not p.get("thought", False):
                        text += p.get("text", "")
                fr = c.get("finishReason")
                if fr != None and fr != "":
                    if fr not in ["STOP", "MAX_TOKENS"]:
                        fail("llm provider: generation stopped without text completion")
                    finish = fr
            if finish != "":
                done = {"type": "done", "finish_reason": finish, "usage": udict}
                events.close()  # release the network as soon as terminal is parsed
                if text != "":
                    # deliver the delta first; done follows on next pull
                    state["pending"] = done
                    return (True, {"type": "text_delta", "text": text})
                state["done"] = True
                return (True, done)
            if text != "":
                return (True, {"type": "text_delta", "text": text})

    return _streams.new(nxt)
