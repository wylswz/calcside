# neon

Parameterized SQL over Neon's HTTP endpoint (`POST /sql`, `$1..$n` params).
Setup: project host (e.g. `ep-....neon.tech`) in `config.host`, the role
password as `NEON_PASSWORD` allowed for that host, and the host in
`net.allow_hosts`.
