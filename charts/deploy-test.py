#!/usr/bin/env python3
"""One-command test deploy of the calcside chart into the current kubectl
context. Every secret value (worker shared key, vault key, admin password,
PostgreSQL password) is generated at run time — nothing secret is committed.

Usage:
  charts/deploy-test.py                      # postgres + chart, namespace "calcside"
  charts/deploy-test.py -n test -r demo      # custom namespace/release
  charts/deploy-test.py --uninstall          # tear down release and test postgres
  charts/deploy-test.py --values-only        # only emit the values file (old behavior)

By default a disposable in-cluster PostgreSQL (Deployment+Service+Secret on
an emptyDir) is applied first and the chart points at it. Use --no-postgres
and the TEST_PG_* variables to target an existing database instead:
  TEST_PG_HOST (default "postgres")        TEST_PG_PORT (default 5432)
  TEST_PG_USER (default "calcside")        TEST_PG_DATABASE (default "calcside")
  TEST_PG_PASSWORD (generated; reused from the postgres-test Secret on rerun)
  TEST_PG_SSLMODE (default "disable")
  TEST_ADMIN_USERNAME (default "admin")    TEST_ADMIN_PASSWORD (generated)
  TEST_WORKER_KEY / TEST_VAULT_KEY (generated; reused from the live release)
  TEST_CONSOLE_ORIGIN (default "http://localhost:8080")
  TEST_API_REPLICAS (default 1)            TEST_PERSISTENCE (default "true")
  TEST_SERVICE_TYPE (default "ClusterIP", e.g. "LoadBalancer")
  TEST_IMAGE_TAG (set image.tag and migrations.image.tag, e.g. "dev")

Manifests are emitted as JSON documents (a YAML subset) joined by `---`
document markers, so kubectl/helm consume them without a YAML dependency.
"""

import argparse
import base64
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import urllib.parse

CHART_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "calcside")


def die(message):
    print(f"deploy-test: {message}", file=sys.stderr)
    sys.exit(1)


def run(argv, *, input_text=None, capture=False, check=True, env=None):
    result = subprocess.run(
        argv,
        input=input_text,
        capture_output=capture,
        text=True,
        env=env,
        check=False,
    )
    if check and result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip() if capture else ""
        die(f"command failed ({result.returncode}): {' '.join(argv)}" +
            (f"\n{detail}" if detail else ""))
    return result


def tool_output(argv):
    result = subprocess.run(argv, capture_output=True, text=True, check=False)
    if result.returncode != 0:
        return ""
    return result.stdout.strip()


def write_private(path, text):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w") as handle:
        handle.write(text)


def read_secret(namespace, name, key):
    raw = tool_output(
        ["kubectl", "get", "secret", name, "-n", namespace,
         "-o", f"jsonpath={{.data['{key}']}}"])
    if not raw:
        return ""
    try:
        return base64.b64decode(raw).decode()
    except Exception:
        return ""


def secret_exists(namespace, name):
    return subprocess.run(
        ["kubectl", "get", "secret", name, "-n", namespace],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False,
    ).returncode == 0


def docs_stream(documents):
    """Encode documents as a multi-doc YAML stream of JSON objects."""
    return "\n---\n".join(json.dumps(doc, indent=2) for doc in documents) + "\n"


def pg_manifest(cfg):
    secret = {
        "apiVersion": "v1",
        "kind": "Secret",
        "metadata": {"name": "postgres-test",
                     "labels": {"app.kubernetes.io/name": "postgres-test"}},
        "type": "Opaque",
        "stringData": {"password": cfg["pg_password"]},
    }
    deployment = {
        "apiVersion": "apps/v1",
        "kind": "Deployment",
        "metadata": {"name": cfg["pg_host"],
                     "labels": {"app.kubernetes.io/name": "postgres-test"}},
        "spec": {
            "replicas": 1,
            "strategy": {"type": "Recreate"},
            "selector": {"matchLabels": {"app.kubernetes.io/name": "postgres-test"}},
            "template": {
                "metadata": {"labels": {"app.kubernetes.io/name": "postgres-test"}},
                "spec": {
                    "containers": [{
                        "name": "postgres",
                        "image": "postgres:17-alpine",
                        "env": [
                            {"name": "POSTGRES_USER", "value": cfg["pg_user"]},
                            {"name": "POSTGRES_DB", "value": cfg["pg_db"]},
                            {"name": "POSTGRES_PASSWORD", "valueFrom": {
                                "secretKeyRef": {"name": "postgres-test",
                                                 "key": "password"}}},
                        ],
                        "ports": [{"containerPort": 5432}],
                        "readinessProbe": {
                            "exec": {"command": ["pg_isready", "-U", cfg["pg_user"],
                                                 "-d", cfg["pg_db"]]},
                            "initialDelaySeconds": 3,
                            "periodSeconds": 5,
                        },
                        "volumeMounts": [{"name": "data",
                                          "mountPath": "/var/lib/postgresql/data"}],
                    }],
                    "volumes": [{"name": "data", "emptyDir": {}}],
                },
            },
        },
    }
    service = {
        "apiVersion": "v1",
        "kind": "Service",
        "metadata": {"name": cfg["pg_host"],
                     "labels": {"app.kubernetes.io/name": "postgres-test"}},
        "spec": {
            "selector": {"app.kubernetes.io/name": "postgres-test"},
            "ports": [{"port": cfg["pg_port"], "targetPort": 5432}],
        },
    }
    return docs_stream([secret, deployment, service])


def gen_values(cfg):
    values = {
        "replicaCount": cfg["api_replicas"],
        "persistence": {"enabled": cfg["persistence"]},
        "service": {"type": cfg["service_type"]},
        "database": {
            "driver": "postgres",
            "postgres": {
                "dsn": (f"postgres://{urllib.parse.quote(cfg['pg_user'], safe='')}"
                        f":{urllib.parse.quote(cfg['pg_password'], safe='')}"
                        f"@{cfg['pg_host']}:{cfg['pg_port']}"
                        f"/{urllib.parse.quote(cfg['pg_db'], safe='')}?"
                        + urllib.parse.urlencode(
                            {"sslmode": cfg["pg_sslmode"], "search_path": "public"})),
            },
        },
        "config": {"consoleOrigin": cfg["console_origin"],
                   "adminUsername": cfg["admin_user"]},
        "secrets": {"workerSharedKey": cfg["worker_key"],
                    "secretKey": cfg["vault_key"],
                    "adminPassword": cfg["admin_password"]},
    }
    if cfg["image_tag"]:
        values["image"] = {"tag": cfg["image_tag"]}
        values["migrations"] = {"image": {"tag": cfg["image_tag"]}}
    return json.dumps(values, indent=2) + "\n"


def main():
    env = os.environ
    parser = argparse.ArgumentParser(
        prog="deploy-test.py",
        description="Deploy the calcside chart (plus a disposable in-cluster "
                    "PostgreSQL) into the current kubectl context.")
    parser.add_argument("-r", "--release", default=env.get("RELEASE", "calcside"))
    parser.add_argument("-n", "--namespace", default=env.get("NAMESPACE", "calcside"))
    parser.add_argument("--no-postgres", action="store_true")
    parser.add_argument("--no-wait", action="store_true")
    parser.add_argument("--values-only", action="store_true")
    parser.add_argument("--uninstall", action="store_true")
    parser.add_argument("-o", "--output", default="")
    parser.add_argument("--postgres-manifest", default="")
    args = parser.parse_args()

    if not args.values_only:
        for tool in ("helm", "kubectl"):
            if shutil.which(tool) is None:
                die(f"{tool} is required")

    cfg = {
        "release": args.release,
        "namespace": args.namespace,
        "pg_host": env.get("TEST_PG_HOST", "postgres"),
        "pg_port": int(env.get("TEST_PG_PORT", "5432")),
        "pg_user": env.get("TEST_PG_USER", "calcside"),
        "pg_db": env.get("TEST_PG_DATABASE", "calcside"),
        "pg_sslmode": env.get("TEST_PG_SSLMODE", "disable"),
        "admin_user": env.get("TEST_ADMIN_USERNAME", "admin"),
        "console_origin": env.get("TEST_CONSOLE_ORIGIN", "http://localhost:8080"),
        "api_replicas": int(env.get("TEST_API_REPLICAS", "1")),
        "persistence": env.get("TEST_PERSISTENCE", "true") == "true",
        "service_type": env.get("TEST_SERVICE_TYPE", "ClusterIP"),
        "image_tag": env.get("TEST_IMAGE_TAG", ""),
        "pg_password": env.get("TEST_PG_PASSWORD", ""),
        "admin_password": env.get("TEST_ADMIN_PASSWORD", ""),
        "worker_key": env.get("TEST_WORKER_KEY", ""),
        "vault_key": env.get("TEST_VAULT_KEY", ""),
    }

    # Reuse the running test database's password so redeploys keep working.
    if (not cfg["pg_password"] and not args.values_only and not args.no_postgres
            and secret_exists(cfg["namespace"], "postgres-test")):
        cfg["pg_password"] = read_secret(cfg["namespace"], "postgres-test", "password")
    cfg["pg_password"] = cfg["pg_password"] or secrets.token_hex(24)

    # Reuse the live release's generated secrets on redeploy: rotating the vault
    # key orphans encrypted rows, and rotating the admin password changes the
    # bootstrap credential of the persisted admin account.
    fullname = cfg["release"] if "calcside" in cfg["release"] else f"{cfg['release']}-calcside"
    chart_secrets = f"{fullname}-secrets"
    if not args.values_only and secret_exists(cfg["namespace"], chart_secrets):
        cfg["worker_key"] = cfg["worker_key"] or read_secret(
            cfg["namespace"], chart_secrets, "worker-shared-key")
        cfg["vault_key"] = cfg["vault_key"] or read_secret(
            cfg["namespace"], chart_secrets, "secret-key")
        cfg["admin_password"] = cfg["admin_password"] or read_secret(
            cfg["namespace"], chart_secrets, "admin-password")
    cfg["worker_key"] = cfg["worker_key"] or base64.b64encode(secrets.token_bytes(32)).decode()
    cfg["vault_key"] = cfg["vault_key"] or base64.b64encode(secrets.token_bytes(32)).decode()
    cfg["admin_password"] = cfg["admin_password"] or secrets.token_hex(24)

    if args.uninstall:
        run(["helm", "uninstall", cfg["release"], "-n", cfg["namespace"]], check=False)
        if not args.no_postgres:
            run(["kubectl", "delete", "-n", cfg["namespace"], "--ignore-not-found",
                 "-f", "-"], input_text=pg_manifest(cfg), check=False)
        return

    if args.values_only:
        if args.output:
            write_private(args.output, gen_values(cfg))
            print(f"wrote {args.output}", file=sys.stderr)
        else:
            sys.stdout.write(gen_values(cfg))
        if args.postgres_manifest:
            write_private(args.postgres_manifest, pg_manifest(cfg))
            print(f"wrote {args.postgres_manifest}", file=sys.stderr)
        return

    with tempfile.TemporaryDirectory() as tmp:
        values_path = os.path.join(tmp, "values.json")
        write_private(values_path, gen_values(cfg))
        if args.output:
            write_private(args.output, gen_values(cfg))

        namespace_doc = docs_stream([{
            "apiVersion": "v1",
            "kind": "Namespace",
            "metadata": {"name": cfg["namespace"]},
        }])
        run(["kubectl", "apply", "-f", "-"], input_text=namespace_doc)

        if not args.no_postgres:
            run(["kubectl", "apply", "-n", cfg["namespace"], "-f", "-"],
                input_text=pg_manifest(cfg))
            if args.postgres_manifest:
                write_private(args.postgres_manifest, pg_manifest(cfg))
            run(["kubectl", "rollout", "status", f"deployment/{cfg['pg_host']}",
                 "-n", cfg["namespace"], "--timeout=180s"])

        helm = ["helm", "upgrade", "--install", cfg["release"], CHART_DIR,
                "-n", cfg["namespace"], "--create-namespace", "-f", values_path]
        if not args.no_wait:
            helm += ["--wait", "--timeout", "5m"]
        run(helm)

    svc = cfg["release"] if "calcside" in cfg["release"] else f"{cfg['release']}-calcside"
    print(f"""
Deployed release "{cfg['release']}" in namespace "{cfg['namespace']}".

  admin user:     {cfg['admin_user']}
  admin password: {cfg['admin_password']}

  kubectl port-forward -n {cfg['namespace']} svc/{svc} 8080:8080
  open {cfg['console_origin']}

Tear down with: charts/deploy-test.py --uninstall -n {cfg['namespace']} -r {cfg['release']}
""")


if __name__ == "__main__":
    main()
