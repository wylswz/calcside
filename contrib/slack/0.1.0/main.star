def _api(path, params=None, body=None):
    u = config["base_url"] + path
    if params != None:
        u += "?" + url.query_encode(params)
    headers = {"Authorization": "Bearer " + config["bot_token"]}
    if body == None:
        r = net.get(url=u, headers=headers)
    else:
        r = net.post(url=u, body=json.encode(body), headers=headers,
                     content_type="application/json; charset=utf-8")
    if r["status"] < 200 or r["status"] >= 300:
        fail("slack api: status " + str(r["status"]))
    data = json.decode(r["body"])
    if not data.get("ok"):
        fail("slack api: " + str(data.get("error", "unknown_error")))
    return data


def post_message(channel, text, thread_ts=None):
    body = {"channel": channel, "text": text}
    if thread_ts != None:
        body["thread_ts"] = thread_ts
    data = _api("/chat.postMessage", body=body)
    return {"ts": data["ts"], "channel": data["channel"]}


def list_channels(limit=50):
    data = _api("/conversations.list", params={"limit": str(limit)})
    return [{"id": c["id"], "name": c["name"], "is_member": c.get("is_member", False)}
            for c in data["channels"]]


def channel_history(channel, limit=20):
    data = _api("/conversations.history",
                params={"channel": channel, "limit": str(limit)})
    return [{"ts": m["ts"], "user": m.get("user", ""), "text": m.get("text", "")}
            for m in data["messages"]]


def add_reaction(channel, timestamp, emoji):
    _api("/reactions.add",
         body={"channel": channel, "timestamp": timestamp, "name": emoji})
    return True
