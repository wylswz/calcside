def _api(path, method="GET", body=None, params=None):
    u = config["base_url"] + path
    if params != None:
        u += "?" + url.query_encode(params)
    headers = {
        "Authorization": "Bearer " + config["token"],
        "Accept": "application/vnd.github+json",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    if body == None:
        r = net.request(method=method, url=u, headers=headers)
    else:
        r = net.post(url=u, body=json.encode(body), headers=headers,
                     content_type="application/json")
    if r["status"] < 200 or r["status"] >= 300:
        fail("github api: status " + str(r["status"]) + " for " + method + " " + path)
    return json.decode(r["body"])


def get_repo(owner, repo):
    d = _api("/repos/" + owner + "/" + repo)
    return {
        "full_name": d["full_name"],
        "description": d.get("description", ""),
        "default_branch": d["default_branch"],
        "stars": d["stargazers_count"],
        "private": d["private"],
    }


def _issue_summary(d):
    return {
        "number": d["number"],
        "title": d["title"],
        "state": d["state"],
        "user": d["user"]["login"],
        "labels": [l["name"] for l in d.get("labels", [])],
    }


def list_issues(owner, repo, state="open", limit=20):
    ds = _api("/repos/" + owner + "/" + repo + "/issues",
              params={"state": state, "per_page": str(limit)})
    return [_issue_summary(d) for d in ds]


def create_issue(owner, repo, title, body="", labels=[]):
    d = _api("/repos/" + owner + "/" + repo + "/issues",
             body={"title": title, "body": body, "labels": labels})
    return {"number": d["number"], "html_url": d["html_url"]}


def create_comment(owner, repo, number, body):
    d = _api("/repos/" + owner + "/" + repo + "/issues/" + str(number) + "/comments",
             body={"body": body})
    return {"id": d["id"], "html_url": d["html_url"]}


def list_pull_requests(owner, repo, state="open", limit=20):
    ds = _api("/repos/" + owner + "/" + repo + "/pulls",
              params={"state": state, "per_page": str(limit)})
    return [{
        "number": d["number"], "title": d["title"], "state": d["state"],
        "user": d["user"]["login"], "head": d["head"]["ref"],
    } for d in ds]


def get_file(owner, repo, path):
    u = config["base_url"] + "/repos/" + owner + "/" + repo + "/contents/" + path
    headers = {
        "Authorization": "Bearer " + config["token"],
        "Accept": "application/vnd.github.raw",
        "X-GitHub-Api-Version": "2022-11-28",
    }
    r = net.get(url=u, headers=headers)
    if r["status"] < 200 or r["status"] >= 300:
        fail("github api: status " + str(r["status"]) + " for GET " + path)
    body = r["body"]
    if len(body) > config["max_file_bytes"]:
        fail("github api: file too large")
    return {"path": path, "size": len(body), "content": body}
