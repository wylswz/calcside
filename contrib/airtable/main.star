def _api(path, method="GET", body=None, params=None):
    u = config["base_url"] + path
    if params != None:
        u += "?" + url.query_encode(params)
    headers = {"Authorization": "Bearer " + config["api_key"]}
    if body != None:
        headers["Content-Type"] = "application/json"
        body = json.encode(body)
    r = net.request(method=method, url=u, body=body, headers=headers)
    if r["status"] < 200 or r["status"] >= 300:
        fail("airtable api: status " + str(r["status"]) + " for " + method + " " + path)
    if r["body"] == "":
        return {}
    return json.decode(r["body"])


def _records(d):
    return [{"id": r["id"], "fields": r["fields"]} for r in d.get("records", [])]


def list_records(base_id, table, max_records=20, view="", filter_by_formula=""):
    params = {"maxRecords": str(max_records)}
    if view != "":
        params["view"] = view
    if filter_by_formula != "":
        params["filterByFormula"] = filter_by_formula
    d = _api("/v0/" + base_id + "/" + url.path_escape(table), params=params)
    return _records(d)


def create_records(base_id, table, records):
    d = _api("/v0/" + base_id + "/" + url.path_escape(table),
             method="POST", body={"records": [{"fields": f} for f in records]})
    return _records(d)


def update_records(base_id, table, records):
    d = _api("/v0/" + base_id + "/" + url.path_escape(table),
             method="PATCH", body={"records": records})
    return _records(d)
