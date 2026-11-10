def _api(path, method="GET", body=None):
    u = config["base_url"] + path
    headers = {
        "Authorization": "Bearer " + config["api_token"],
        "Notion-Version": config["notion_version"],
    }
    if body == None:
        r = net.request(method=method, url=u, headers=headers)
    else:
        r = net.post(url=u, body=json.encode(body), headers=headers,
                     content_type="application/json")
    if r["status"] < 200 or r["status"] >= 300:
        fail("notion api: status " + str(r["status"]) + " for " + method + " " + path)
    return json.decode(r["body"])


def _title(d):
    props = d.get("properties", {})
    for k in props:
        p = props[k]
        if p.get("type") == "title":
            return "".join([t.get("plain_text", "") for t in p["title"]])
    return ""


def search(query="", limit=10):
    d = _api("/v1/search", body={"query": query, "page_size": limit})
    return [{"id": o["id"], "object": o["object"], "title": _title(o),
             "url": o.get("url", "")} for o in d["results"]]


def get_page(page_id):
    d = _api("/v1/pages/" + page_id)
    return {"id": d["id"], "url": d["url"], "title": _title(d)}


def query_database(database_id, filter=None, limit=20):
    body = {"page_size": limit}
    if filter != None:
        body["filter"] = filter
    d = _api("/v1/databases/" + database_id + "/query", body=body)
    return [{"id": o["id"], "url": o["url"], "title": _title(o),
             "props": o.get("properties", {})} for o in d["results"]]


def create_page(parent_id, title, content=""):
    children = []
    if content != "":
        children = [{
            "object": "block", "type": "paragraph",
            "paragraph": {"rich_text": [{"type": "text", "text": {"content": content}}]},
        }]
    d = _api("/v1/pages", body={
        "parent": {"page_id": parent_id},
        "properties": {"title": {"title": [{"text": {"content": title}}]}},
        "children": children,
    })
    return {"id": d["id"], "url": d["url"]}
