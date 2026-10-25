def graphql(query, variables=None):
    body = {"query": query}
    if variables != None:
        body["variables"] = variables
    r = net.post(
        url=config["base_url"],
        body=json.encode(body),
        headers={"Authorization": config["api_key"]},
        content_type="application/json",
    )
    if r["status"] < 200 or r["status"] >= 300:
        fail("linear api: status " + str(r["status"]))
    data = json.decode(r["body"])
    if data.get("errors"):
        fail("linear api: " + str(data["errors"][0].get("message", "graphql error")))
    return data["data"]


def list_teams():
    d = graphql("{ teams { nodes { id key name } } }")
    return d["teams"]["nodes"]


def create_issue(team_id, title, description=""):
    d = graphql(
        "mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier url } } }",
        {"input": {"teamId": team_id, "title": title, "description": description}},
    )
    if not d["issueCreate"]["success"]:
        fail("linear api: issueCreate failed")
    return d["issueCreate"]["issue"]


def list_issues(limit=20):
    d = graphql(
        "query($n: Int) { issues(first: $n) { nodes { id identifier title state { name } assignee { displayName } } } }",
        {"n": limit},
    )
    return [{
        "id": i["id"], "identifier": i["identifier"], "title": i["title"],
        "state": i["state"]["name"] if i.get("state") else "",
        "assignee": i["assignee"]["displayName"] if i.get("assignee") else "",
    } for i in d["issues"]["nodes"]]
