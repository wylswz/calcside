def _api(path, method="GET", body=None):
    host = config["host"]
    if host == "":
        fail("qdrant: config host is required")
    if "/" in host or "@" in host or ":" in host:
        fail("qdrant: host must be a bare hostname")
    headers = {"api-key": config["api_key"]}
    if body != None:
        headers["Content-Type"] = "application/json"
        body = json.encode(body)
    r = net.request(method=method, url="https://" + host + path,
                    body=body, headers=headers)
    if r["status"] < 200 or r["status"] >= 300:
        fail("qdrant: status " + str(r["status"]) + " for " + method + " " + path)
    return json.decode(r["body"])


def list_collections():
    d = _api("/collections")
    return [c["name"] for c in d["result"]["collections"]]


def describe_collection(collection):
    d = _api("/collections/" + url.path_escape(collection))
    r = d["result"]
    return {"status": r["status"], "vectors_count": r.get("vectors_count", 0),
            "points_count": r.get("points_count", 0)}


def search(collection, vector, limit=10, with_payload=True):
    d = _api("/collections/" + url.path_escape(collection) + "/points/search",
             method="POST",
             body={"vector": vector, "limit": limit, "with_payload": with_payload})
    return [{"id": p["id"], "score": p["score"],
             "payload": p.get("payload", {})} for p in d["result"]]


def scroll(collection, limit=20, offset=None, with_payload=True):
    body = {"limit": limit, "with_payload": with_payload, "with_vector": False}
    if offset != None:
        body["offset"] = offset
    d = _api("/collections/" + url.path_escape(collection) + "/points/scroll",
             method="POST", body=body)
    r = d["result"]
    return {"points": r["points"], "next_page_offset": r.get("next_page_offset")}


def upsert(collection, points):
    d = _api("/collections/" + url.path_escape(collection) + "/points?wait=true",
             method="PUT", body={"points": points})
    return d["result"]["status"]
