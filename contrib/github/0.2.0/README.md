# github 0.2.0

GitHub REST API v2022-11-28 plus one fixed-shape GraphQL query for
contribution totals. All traffic goes through the instance's gated `net`
capability - `allow_hosts`, method whitelists, secret domain allowlists,
policies, and audit apply unchanged. Ops use GET and POST only.

## Auth and scopes

`config.token` is a `type: secret` field: pass a `{{secrets.NAME}}`
reference and the real value is injected into the `Authorization` header
at send time, redacted in responses and audit. Set it to `""` for
unauthenticated access to public data (60 req/hour core, 10 req/min
search - much lower than authenticated limits).

Token choice:

- **Fine-grained PAT** (recommended): grant only the repositories you
  query. Read ops need **Contents: read**, **Issues: read**,
  **Pull requests: read**, **Metadata: read** (mandatory). Write ops
  (`create_issue`, `create_comment`) additionally need **Issues: read
  and write**. GraphQL `get_contributions` works with fine-grained PATs.
- **Classic PAT**: no scope is needed for public reads; `repo` grants
  private-repo access (read+write); add `read:org` for authenticated
  org-membership visibility.

Grant `net.allow_hosts` for the HTTPS GitHub Enterprise REST API host
when `base_url` points there (e.g. `ghe.example.com` or
`ghe.example.com/api/v3` per your Enterprise layout). Secrets over plain
HTTP are normally disallowed, so use HTTPS. Note `get_contributions`
posts to `{base_url}/graphql`, which matches api.github.com's layout;
GitHub Enterprise REST prefixes like `/api/v3` do not line up with that
endpoint, so contributions effectively require an api.github.com-
compatible GraphQL URL.

## Coverage

Read: users, orgs, events, repos, languages, commits, commit detail,
contributors, git trees (recursive, `truncated` preserved), README
metadata, pull requests, reviews, issue/PR comments, rate limits, and
repository/issue/commit search. Write: `create_issue`,
`create_comment` only - there are deliberately no update/delete/star/fork
ops.

`get_contributions` is the only GraphQL call and uses a fixed query -
no arbitrary GraphQL. It returns `contributionsCollection` totals plus
the query-level `rateLimit`. Caveats from GitHub itself:

- The `from`/`to` window is limited to **<= 1 year** span; split longer
  ranges and call repeatedly.
- `hasAnyRestrictedContributions` /
  `restrictedContributionsCount` mark activity on private repos the
  token cannot see - totals are visibility-limited, never a complete
  account history.
- Commit contributions count commits **authored or co-authored** on the
  default branch - they are not a count of all commits.
- `*ContributionsByRepository` lists are capped at **100 repositories**
  each.

`list_events` is likewise bounded by GitHub: only recent events (~90
days, ~300 events) are exposed - not complete history. `get_tree` can
return `truncated: true` on very large trees; the flag is preserved, not
hidden. Search endpoints cap results at the first **1000** matches
(`page * per_page <= 1000`, enforced client-side; `window_limit` is
reported in the result).

## Pagination

Paged ops (`list_repos`, `list_orgs`, `list_events`, `list_commits`,
`list_contributors`, `list_reviews`, `list_issue_comments`, and the three
`search_*` ops) return one page at a time:

```python
page = {"has_more": True, "next_page": 1}
n = 0
while page["has_more"] and n < 5:            # bounded stop: max 5 pages
    page = ext.github.list_commits("octocat", "Hello-World",
                                   page=page["next_page"], per_page=100)
    for c in page["items"]:
        print(c["sha"][:8])
    n += 1
```

`next_page` is `page + 1` **only** when the response `Link` header has
`rel="next"`; returned `Link` URLs are never followed. `has_more=false`
means stop. `per_page` is bounded 1..100, `page` must be a positive
integer - invalid values fail before any network call. Search adds
`total_count`, `incomplete_results`, and `window_limit: 1000`; the last
reachable page (`page * per_page >= 1000`) always reports
`has_more=false`.

There is **no auto-iteration, sleeping, or transparent retry** - pace
calls yourself and honor `rate_limit` / error diagnostics.

## Failures and rate limits

Non-2xx responses fail with `github api: status <n> for <METHOD> <path>`
plus `remaining=`, `reset=`, `retry_after=` from the response headers
(`unknown` when absent). 403 can mean missing permissions as well as
rate limiting; 429 is the rate-limit signal - check `get_rate_limit()`
or the `rate_limit` object every paged result carries
(`remaining`, `reset`, `resource`). Authenticated core quota is 5000/hr,
search 30 req/min, GraphQL has its own points budget (`cost` in
`get_contributions`' `rate_limit`).

Path segments (`owner`, `repo`, `username`, `ref`, file `path`) are
validated and percent-escaped - empty, `.`, `..`, `/`-containing (for
single segments) and backslash values fail before any request, so they
cannot escape the API path. `get_file`'s `path` keeps `/` separators but
rejects traversal segments; its `ref` travels as a query parameter, so
branch names containing `/` work there.
