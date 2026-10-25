def _url(path):
    host = config["host"]
    if host == "":
        fail("upstash_redis: config host is required")
    if "/" in host or "@" in host or ":" in host:
        fail("upstash_redis: host must be a bare hostname")
    return "https://" + host + path


def command(args):
    if type(args) != "list" or len(args) == 0:
        fail("upstash_redis: args must be a non-empty list")
    r = net.post(url=_url("/"), body=json.encode(args),
                 headers={"Authorization": "Bearer " + config["token"]},
                 content_type="application/json")
    if r["status"] < 200 or r["status"] >= 300:
        fail("upstash_redis: status " + str(r["status"]))
    d = json.decode(r["body"])
    if "error" in d:
        fail("upstash_redis: " + str(d["error"]))
    return d.get("result")


def pipeline(commands):
    if type(commands) != "list" or len(commands) == 0:
        fail("upstash_redis: commands must be a non-empty list")
    r = net.post(url=_url("/pipeline"), body=json.encode(commands),
                 headers={"Authorization": "Bearer " + config["token"]},
                 content_type="application/json")
    if r["status"] < 200 or r["status"] >= 300:
        fail("upstash_redis: status " + str(r["status"]))
    out = []
    for d in json.decode(r["body"]):
        if "error" in d:
            fail("upstash_redis: " + str(d["error"]))
        out.append(d.get("result"))
    return out
