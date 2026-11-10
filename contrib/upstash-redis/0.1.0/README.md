# upstash_redis

Any Redis command over Upstash's REST endpoint — no raw TCP needed.
Setup: database REST host (bare hostname, no scheme) in `config.host`,
REST token as `UPSTASH_REDIS_TOKEN` allowed for that host, and
`net.allow_hosts` containing the same host.
