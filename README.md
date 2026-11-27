# calcside

A lightweight code-execution sandbox for AI agents, built on Starlark.

Inspired by [citron](https://github.com/mishudark/citron) and [marimo](https://marimo.io/)

Powered by [starlark-go](https://github.com/google/starlark-go)


## Get started

**Use a new database for this release.** It does not support in-place upgrades
from v0.0.7 or earlier. The application migration removes the legacy script
column without converting it to notebook sections. Keep the old deployment or
a backup if you need to recover existing applications; do not point the new
deployment at the old database.

```bash
./docker/init-env.sh && cd docker && docker compose up --build
```

Then open http://localhost:8080.
