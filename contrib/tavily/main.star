def search(query, max_results=None):
    """POST {base_url}/search and return the results list."""
    if max_results == None:
        max_results = config["max_results"]
    body = json.encode({
        "query": query,
        "max_results": max_results,
        "search_depth": config["search_depth"],
    })
    r = net.post(
        url=config["base_url"] + "/search",
        body=body,
        headers={"Authorization": "Bearer " + config["api_key"]},
    )
    if r["status"] < 200 or r["status"] >= 300:
        fail("tavily search failed: status " + str(r["status"]))
    return json.decode(r["body"])["results"]
