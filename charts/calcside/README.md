# calcside Helm chart

Deploys the calcside API and one or more execution workers.

## Install

```bash
helm install calcside oci://ghcr.io/wylswz/charts/calcside \
  --version 0.0.1 \
  --namespace calcside --create-namespace \
  --set config.consoleOrigin=https://calcside.example.com \
  --set ingress.enabled=true \
  --set ingress.host=calcside.example.com
```

The chart defaults to SQLite on a PersistentVolumeClaim with one API replica and
one worker. For production, use PostgreSQL and multiple API replicas.

## Images

The release workflow publishes:

| Image | Purpose |
| --- | --- |
| `ghcr.io/wylswz/calcside:<version>` | API (`calcside serve`) and worker (`calcside-worker serve`) |
| `ghcr.io/wylswz/calcside-migrate:<version>` | Atlas migrations bundled with both dialect directories |

`image.tag` and `migrations.image.tag` default to `.Chart.AppVersion`, so a
packaged chart points at the matching image tag. Override them for local builds:

```bash
--set image.tag=dev --set migrations.image.tag=dev
```

GHCR packages are private by default. Make them public or set
`imagePullSecrets` to a Secret that can pull from `ghcr.io`.

## Database

SQLite is the default and requires `replicaCount=1`. Migrations run in an init
container on the API pod before the server starts. Disable them with
`migrations.enabled=false` only if you apply migrations outside the chart.

PostgreSQL:

```bash
kubectl create secret generic calcside-postgres \
  --from-literal=dsn='postgres://user:pass@db:5432/calcside?sslmode=require&search_path=public'

helm install calcside oci://ghcr.io/wylswz/charts/calcside \
  --set database.driver=postgres \
  --set database.postgres.existingSecret=calcside-postgres \
  --set replicaCount=2
```

The PostgreSQL DSN is also used as `ATLAS_DB_URL`; include `search_path=public`.
For multiple API replicas, either use a ReadWriteMany data volume or set
`persistence.enabled=false` (local policies and the extension cache are then
ephemeral). The database itself is external.

## Testing

`charts/deploy-test.py` (repo tooling, not packaged with the chart) performs a
one-command test deploy into the current kubectl context. It generates every
secret at run time — worker shared key, vault key, admin password, and a
PostgreSQL DSN — applies a disposable in-cluster PostgreSQL
(Deployment+Service+Secret on an emptyDir) that shares the generated password,
then runs `helm upgrade --install` and prints the admin credentials plus a
port-forward hint:

```bash
charts/deploy-test.py                      # deploy into namespace "calcside"
charts/deploy-test.py --uninstall          # tear down release and test postgres
charts/deploy-test.py --values-only        # just emit the generated values
charts/deploy-test.py --no-postgres        # use TEST_PG_* to target an existing DB
```

`TEST_PG_*`/`TEST_ADMIN_*`/`TEST_*` environment variables override the generated
defaults; `TEST_IMAGE_TAG` overrides both image tags (e.g. for `kind load`).

## Secrets

By default the chart creates `Secret/<release>-secrets` containing the generated
worker shared key plus any configured optional keys:

| Key | Used for |
| --- | --- |
| `worker-shared-key` | API ↔ worker HMAC |
| `secret-key` | Base64-encoded 32-byte vault key |
| `admin-password` | Bootstrap administrator password |
| `google-client-secret` | Google OAuth client secret |

Set `secrets.existingSecret` to use your own Secret instead. The generated worker
key is preserved across `helm upgrade` via `lookup`; for GitOps, manage the
Secret yourself.

Bootstrap administrator:

```bash
--set config.adminUsername=admin \
--set secrets.adminPassword='a-long-password'
```

## Workers

Workers are a StatefulSet; each pod's name is its node ID and is pre-registered
in the API's `--workers` list. The headless Service is
`<release>-calcside-worker`. Scaling the StatefulSet updates the API worker list
and rolls the API.

Workers run with process-per-instance isolation. Per-instance cgroup memory caps
require a writable cgroup v2 subtree, which unprivileged Kubernetes pods usually
do not have; `worker.instanceMemoryMax` therefore defaults to `0`. The heap
watchdog (`limits.execMemoryLimit`) and the pod memory limit still apply. Leave it
at `0` unless your platform grants writable cgroups.

## Extensions and policies

The API data volume holds `/data/policies`, `/data/ext`, and `/data/ext-cache`.
Copy global `.rego` files or local extension directories into the volume (for
example with `kubectl cp`) or use `persistence.existingClaim` with a pre-populated
volume. Workers resolve local extension sources through the API and keep their
own fetch cache under `/cache`.

## Ingress and previews

`ingress.enabled` exposes the API Service. Use `config.consoleOrigin` for the
public URL and set `config.cookieSecure=true` for HTTPS.

Rendered HTML previews require a separate isolated origin on a different
registrable domain plus a wildcard DNS record and certificate:

```bash
--set config.artifactPreviewBaseURL=https://reports.example.net \
--set ingress.preview.enabled=true \
--set ingress.preview.host='*.reports.example.net'
```

Both ingresses point at the same Service; the preview host must be a wildcard.
