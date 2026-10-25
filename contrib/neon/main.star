def query(sql, params=[]):
    host = config["host"]
    if host == "":
        fail("neon: config host is required")
    if "/" in host or "@" in host or ":" in host:
        fail("neon: host must be a bare hostname")
    body = {"query": sql, "params": params, "arrayMode": True, "fullResults": True}
    r = net.post(url="https://" + host + "/sql", body=json.encode(body),
                 headers={"Authorization": "Bearer " + config["password"]},
                 content_type="application/json")
    if r["status"] < 200 or r["status"] >= 300:
        fail("neon: status " + str(r["status"]))
    d = json.decode(r["body"])
    return {
        "fields": [f["name"] for f in d.get("fields", [])],
        "rows": d.get("rows", []),
        "row_count": d.get("rowCount", 0),
    }
