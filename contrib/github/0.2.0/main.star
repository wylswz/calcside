# GitHub REST API v2022-11-28 + a fixed-shape GraphQL contributions query.
# Every call goes through the gated `net` capability; the token is a
# {{secrets.*}} placeholder injected at send time and redacted everywhere.
# Pagination is explicit: callers pass page/per_page and follow
# next_page themselves - there is no auto-iteration, sleeping, or retry.


def _segment(value):
    if type(value) != "string" or not value or value in [".", ".."] or "/" in value or "\\" in value:
        fail("github: expected a nonempty path segment")
    return url.path_escape(value)


def _repo_path(owner, repo):
    return "/repos/" + _segment(owner) + "/" + _segment(repo)


def _file_path(path):
    return "/".join([_segment(p) for p in path.split("/")])


def _headers(accept="application/vnd.github+json"):
    h = {"Accept": accept, "X-GitHub-Api-Version": "2022-11-28"}
    if config["token"]:
        h["Authorization"] = "Bearer " + config["token"]
    return h


def _response(path, method="GET", body=None, params=None, accept="application/vnd.github+json"):
    u = config["base_url"].rstrip("/") + path
    if params:
        u += "?" + url.query_encode({k: str(v) for k, v in params.items() if v != None})
    if body == None:
        r = net.request(method=method, url=u, headers=_headers(accept))
    else:
        method = "POST"
        r = net.post(url=u, body=json.encode(body), headers=_headers(accept), content_type="application/json")
    hs = {k.lower(): v for k, v in r["headers"].items()}
    if r["status"] < 200 or r["status"] >= 300:
        fail("github api: status " + str(r["status"]) + " for " + method + " " + path + "; remaining=" + hs.get("x-ratelimit-remaining", "unknown") + "; reset=" + hs.get("x-ratelimit-reset", "unknown") + "; retry_after=" + hs.get("retry-after", "unknown"))
    return (r, hs)


def _api(path, method="GET", body=None, params=None):
    r, hs = _response(path, method, body, params)
    return json.decode(r["body"])


def _positive(value, name, maximum=None):
    if type(value) != "int" or value < 1 or (maximum != None and value > maximum):
        fail("github: invalid " + name)


def _page(path, params=None, page=1, per_page=100, search=False):
    _positive(page, "page")
    _positive(per_page, "per_page", 100)
    if search and page * per_page > 1000:
        fail("github: search window exceeds 1000; split query by date or repository")
    p = dict(params or {})
    p.update({"page": page, "per_page": per_page})
    r, hs = _response(path, params=p)
    d = json.decode(r["body"])
    more = False
    for link in hs.get("link", "").split(","):
        if 'rel="next"' in link:
            more = True
    if search and page * per_page >= 1000:
        more = False
    out = {"items": d["items"] if search else d, "page": page, "per_page": per_page, "has_more": more, "next_page": page + 1 if more else None, "rate_limit": {"remaining": hs.get("x-ratelimit-remaining"), "reset": hs.get("x-ratelimit-reset"), "resource": hs.get("x-ratelimit-resource")}}
    if search:
        out.update({"total_count": d["total_count"], "incomplete_results": d.get("incomplete_results", False), "window_limit": 1000})
    return out


def _search_params(query, sort, order):
    p = {"q": query}
    if sort != "":
        p["sort"] = sort
    if order != "":
        p["order"] = order
    return p


# --- legacy ops (0.1.0 signatures and result shapes preserved) ---


def get_repo(owner, repo):
    """Repository metadata, reduced to the 0.1.0 field set."""
    d = _api(_repo_path(owner, repo))
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
    """Issue summaries; pull requests returned by the endpoint are filtered out."""
    _positive(limit, "limit", 100)
    ds = _api(_repo_path(owner, repo) + "/issues",
              params={"state": state, "per_page": limit})
    return [_issue_summary(d) for d in ds if d.get("pull_request") == None]


def create_issue(owner, repo, title, body="", labels=[]):
    """Create an issue (POST). Requires a token with issues write access."""
    d = _api(_repo_path(owner, repo) + "/issues",
             body={"title": title, "body": body, "labels": labels})
    return {"number": d["number"], "html_url": d["html_url"]}


def create_comment(owner, repo, number, body):
    """Comment on an issue or pull request (POST)."""
    _positive(number, "number")
    d = _api(_repo_path(owner, repo) + "/issues/" + str(number) + "/comments",
             body={"body": body})
    return {"id": d["id"], "html_url": d["html_url"]}


def list_pull_requests(owner, repo, state="open", limit=20):
    """Pull request summaries."""
    _positive(limit, "limit", 100)
    ds = _api(_repo_path(owner, repo) + "/pulls",
              params={"state": state, "per_page": limit})
    return [{
        "number": d["number"], "title": d["title"], "state": d["state"],
        "user": d["user"]["login"], "head": d["head"]["ref"],
    } for d in ds]


def get_file(owner, repo, path, ref=""):
    """Raw UTF-8 file contents from a ref (default branch when ref is empty).

    path is split on "/" and each segment is validated and escaped, so
    traversal ("..", ".", empty, backslash) is rejected and "?"/"#" cannot
    inject query or fragment parts. ref may contain "/" (branch names) -
    it travels as a query parameter, not a path segment. The response is
    capped at config max_file_bytes.
    """
    u = _repo_path(owner, repo) + "/contents/" + _file_path(path)
    params = {"ref": ref} if ref != "" else None
    r, hs = _response(u, params=params, accept="application/vnd.github.raw+json")
    body = r["body"]
    if len(body) > config["max_file_bytes"]:
        fail("github api: file too large")
    return {"path": path, "size": len(body), "content": body}


# --- identity and activity ---


def get_user(username=""):
    """User profile; the authenticated user when username is empty."""
    if username == "":
        return _api("/user")
    return _api("/users/" + _segment(username))


def get_rate_limit():
    """Current rate-limit quotas for the token (or anonymous caller)."""
    return _api("/rate_limit")


def list_orgs(username="", page=1, per_page=100):
    """Organizations the user belongs to (public list for other users)."""
    if username == "":
        return _page("/user/orgs", page=page, per_page=per_page)
    return _page("/users/" + _segment(username) + "/orgs", page=page, per_page=per_page)


def list_events(username, page=1, per_page=100, public_only=True):
    """Recent activity events for a user.

    GitHub retains only recent events (roughly the last 90 days, capped
    near 300 events) - this is not a complete history. public_only=True
    (default) uses the public events feed; False includes events visible
    only to the authenticated user when username is the token's user.
    """
    path = "/users/" + _segment(username) + "/events"
    if public_only:
        path += "/public"
    return _page(path, page=page, per_page=per_page)


# --- repositories ---


def list_repos(username="", page=1, per_page=100, sort="updated", direction="desc",
              affiliation="owner,collaborator,organization_member"):
    """Repositories visible to the caller.

    Empty username lists repos the token can access (/user/repos,
    affiliation-scoped); a username lists that user's repos
    (/users/{u}/repos, type=owner). Records are raw GitHub JSON - fork,
    archived, language, topics, pushed_at, etc. are preserved.
    """
    if username == "":
        params = {"affiliation": affiliation, "sort": sort, "direction": direction}
        return _page("/user/repos", params=params, page=page, per_page=per_page)
    params = {"type": "owner", "sort": sort, "direction": direction}
    return _page("/users/" + _segment(username) + "/repos",
                 params=params, page=page, per_page=per_page)


def get_languages(owner, repo):
    """Language byte breakdown for the default branch: {language: bytes}."""
    return _api(_repo_path(owner, repo) + "/languages")


def list_commits(owner, repo, author="", since="", until="", sha="",
                 page=1, per_page=100):
    """Commit history on a ref (default branch when sha is empty).

    author/since/until are RFC3339 or GitHub-accepted filters; empty
    values are omitted from the request.
    """
    params = {}
    if author != "":
        params["author"] = author
    if since != "":
        params["since"] = since
    if until != "":
        params["until"] = until
    if sha != "":
        params["sha"] = sha
    return _page(_repo_path(owner, repo) + "/commits", params=params,
                 page=page, per_page=per_page)


def get_commit(owner, repo, ref):
    """Single commit by SHA, tag, or slash-free branch name."""
    return _api(_repo_path(owner, repo) + "/commits/" + _segment(ref))


def list_contributors(owner, repo, page=1, per_page=100):
    """Contributors ordered by commit count (anonymous entries included).

    anon=true is always sent so anonymous contributors (deleted users,
    unmapped emails) appear with login=None. Entries are cached by
    GitHub and reflect the default branch only - they are not a complete
    commit-author census.
    """
    return _page(_repo_path(owner, repo) + "/contributors",
                 params={"anon": "true"}, page=page, per_page=per_page)


def get_tree(owner, repo, ref="HEAD", recursive=True):
    """Git tree object for a ref.

    recursive=True requests the full flattened tree; GitHub may still
    return truncated=true for very large trees - the raw truncated flag
    and entries are preserved, never silently completed.
    """
    params = {"recursive": "1"} if recursive else None
    return _api(_repo_path(owner, repo) + "/git/trees/" + _segment(ref),
                params=params)


def get_readme(owner, repo, ref=""):
    """README metadata JSON for a ref (default branch when ref is empty).

    Returns the raw GitHub object: name, path, sha, size, and the file
    body base64-encoded in content plus a download_url - not the
    rendered text. GitHub wraps the base64 in newlines, so strip them
    before decoding, e.g.
    base64.decode(rm["content"].replace("\\n", "")).decode("utf-8").
    """
    params = {"ref": ref} if ref != "" else None
    return _api(_repo_path(owner, repo) + "/readme", params=params)


# --- issues and pull requests ---


def get_pull_request(owner, repo, number):
    """Full pull request object (raw GitHub JSON)."""
    _positive(number, "number")
    return _api(_repo_path(owner, repo) + "/pulls/" + str(number))


def list_reviews(owner, repo, number, page=1, per_page=100):
    """Reviews on a pull request."""
    _positive(number, "number")
    return _page(_repo_path(owner, repo) + "/pulls/" + str(number) + "/reviews",
                 page=page, per_page=per_page)


def list_issue_comments(owner, repo, number, page=1, per_page=100):
    """Comments on an issue or pull request (shared endpoint)."""
    _positive(number, "number")
    return _page(_repo_path(owner, repo) + "/issues/" + str(number) + "/comments",
                 page=page, per_page=per_page)


# --- search (explicit pages inside GitHub's 1000-result window) ---


def search_repositories(query, page=1, per_page=100, sort="", order="desc"):
    """Search repositories; sort is optional (GitHub default: best match)."""
    return _page("/search/repositories",
                 params=_search_params(query, sort, order),
                 page=page, per_page=per_page, search=True)


def search_issues(query, page=1, per_page=100, sort="updated", order="desc"):
    """Search issues AND pull requests - GitHub's issue search covers both.

    Add is:issue or is:pr to query to restrict the result type; without a
    qualifier both appear in items.
    """
    return _page("/search/issues",
                 params=_search_params(query, sort, order),
                 page=page, per_page=per_page, search=True)


def search_commits(query, page=1, per_page=100, sort="committer-date", order="desc"):
    """Search commits; query follows GitHub commit-search qualifiers."""
    return _page("/search/commits",
                 params=_search_params(query, sort, order),
                 page=page, per_page=per_page, search=True)


# --- contributions (fixed GraphQL query; no arbitrary GraphQL) ---

_CONTRIBUTIONS_QUERY = """
query AccountContributions($login:String!,$from:DateTime!,$to:DateTime!) {
  user(login:$login) {
    contributionsCollection(from:$from,to:$to) {
      startedAt endedAt hasAnyRestrictedContributions restrictedContributionsCount
      totalCommitContributions totalIssueContributions totalPullRequestContributions totalPullRequestReviewContributions totalRepositoryContributions
      contributionCalendar { totalContributions weeks { contributionDays { date contributionCount } } }
      commitContributionsByRepository(maxRepositories:100) { repository { nameWithOwner url isPrivate } contributions { totalCount } }
      pullRequestContributionsByRepository(maxRepositories:100) { repository { nameWithOwner url isPrivate } contributions { totalCount } }
      pullRequestReviewContributionsByRepository(maxRepositories:100) { repository { nameWithOwner url isPrivate } contributions { totalCount } }
    }
  }
  rateLimit { limit remaining resetAt cost }
}
"""


def get_contributions(username, from_time, to_time):
    """Contribution totals for a user between from_time and to_time.

    Both bounds are RFC3339 timestamps passed straight to GitHub's
    from/to arguments - GitHub defines the window semantics (maximum
    span one year). Uses one fixed GraphQL query (variables only - user
    input is never composed into the query text). GitHub limits the
    window to one year and hides contributions on private repos the
    token cannot see (hasAnyRestrictedContributions /
    restrictedContributionsCount flag this). Commit contributions count
    commits authored OR co-authored on the default branch - they are
    NOT a count of all commits, and the per-repository lists are capped
    at 100 entries each. Split longer ranges into <=1-year windows and
    call repeatedly.

    Returns {contributions: <contributionsCollection>, rate_limit:
    <query-level rateLimit>}. GraphQL errors and unknown users fail.
    """
    if type(username) != "string" or username == "":
        fail("github: username must be a nonempty string")
    d = _api("/graphql", method="POST", body={
        "query": _CONTRIBUTIONS_QUERY,
        "variables": {"login": username, "from": from_time, "to": to_time},
    })
    if d.get("errors") != None:
        fail("github graphql: " + json.encode(d["errors"]))
    data = d.get("data")
    if data == None or data.get("user") == None:
        fail("github graphql: user not found")
    return {
        "contributions": data["user"]["contributionsCollection"],
        "rate_limit": data.get("rateLimit"),
    }
