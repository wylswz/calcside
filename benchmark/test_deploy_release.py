#!/usr/bin/env python3
"""Focused unit tests for the release deployment helper.

Run with: PYTHONDONTWRITEBYTECODE=1 python3 benchmark/test_deploy_release.py
All subprocess/network calls are mocked; nothing here touches real clusters,
projects, or the docker daemon.
"""

import importlib.util
import io
import json
import os
import pathlib
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parent.parent


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


deploy_release = load("deploy_release", ROOT / "benchmark" / "deploy-release.py")
diag = load("deploy_release_diag", ROOT / "benchmark" / "deploy_release_diag.py")
deploy_test = load("deploy_test", ROOT / "charts" / "deploy-test.py")


def proc(returncode=0, stdout="", stderr=""):
    return mock.Mock(returncode=returncode, stdout=stdout, stderr=stderr)


def compose_fake(projects="[]", containers="", volumes="",
                 ls_rc=0, ls_err="", api_mapping="127.0.0.1:55001\n"):
    calls = []

    def fake(argv, **kw):
        calls.append((argv, kw))
        if argv[:3] == ["docker", "compose", "ls"]:
            return proc(ls_rc, projects, ls_err)
        if argv[:2] == ["docker", "ps"]:
            return proc(0, containers)
        if argv[:2] == ["docker", "volume"]:
            return proc(0, volumes)
        if argv[-2:] == ["api", "8080"]:
            return proc(0, api_mapping)
        return proc()

    return fake, calls


def kind_fake(clusters="", deploy_rc=0, state=None):
    calls = []

    def fake(argv, **kw):
        calls.append((argv, kw))
        if argv[:2] == ["kind", "get"]:
            return proc(0, clusters)
        if any("deploy-test.py" in str(arg) for arg in argv):
            if state is not None and kw.get("stdout") is not None:
                secrets_env = pathlib.Path(state, "secrets.env")
                if secrets_env.exists():
                    creds = dict(line.split("=", 1) for line in
                                 secrets_env.read_text().splitlines() if "=" in line)
                    kw["stdout"].write(
                        f"admin password: {creds['TEST_ADMIN_PASSWORD']}\n".encode())
                    kw["stdout"].flush()
            return proc(deploy_rc)
        return proc()

    return fake, calls


class StateDirGuard(unittest.TestCase):
    def test_existing_state_dir_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            with mock.patch.object(sys, "argv", ["x", "kind", tmp]), \
                    self.assertRaises(SystemExit) as ctx:
                deploy_release.main()
            self.assertEqual(ctx.exception.code, 1)


class KindGuards(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.state = pathlib.Path(self.tmp.name) / "state"
        self.state.mkdir()

    def tearDown(self):
        self.tmp.cleanup()

    def test_existing_cluster_rejected(self):
        fake, _ = kind_fake(clusters="calcside-release-test\n")
        with mock.patch("subprocess.run", side_effect=fake), \
                mock.patch.object(deploy_release, "need"), \
                self.assertRaises(SystemExit):
            deploy_release.deploy_kind(self.state, "test")
        self.assertFalse((self.state / "backend.env").exists())

    def test_busy_port_rejected(self):
        fake, _ = kind_fake()
        with mock.patch("subprocess.run", side_effect=fake), \
                mock.patch.object(deploy_release, "need"), \
                mock.patch.object(deploy_release, "port_free", return_value=False), \
                self.assertRaises(SystemExit):
            deploy_release.deploy_kind(self.state, "test")
        self.assertFalse((self.state / "backend.env").exists())

    def test_backend_env_written_before_failure(self):
        fake, calls = kind_fake(deploy_rc=1, state=self.state)
        stderr = io.StringIO()
        with mock.patch("subprocess.run", side_effect=fake), \
                mock.patch("subprocess.Popen"), \
                mock.patch.object(deploy_release, "need"), \
                mock.patch.object(deploy_release, "port_free", return_value=True), \
                redirect_stderr(stderr), \
                self.assertRaises(SystemExit):
            deploy_release.deploy_kind(self.state, "test")
        backend = (self.state / "backend.env").read_text()
        self.assertIn("KIND_CLUSTER=calcside-release-test", backend)
        self.assertIn("KIND_PORT=18080", backend)
        # The failure tail must be redacted, not raw deploy.log content.
        creds = json.loads((self.state / "credentials.json").read_text())
        self.assertNotIn(creds["admin_password"], stderr.getvalue())
        self.assertIn("<redacted>", stderr.getvalue())

    def test_kind_flow_state_and_isolated_kubeconfig(self):
        fake, calls = kind_fake(state=self.state)
        forwarder = mock.Mock(pid=4242)
        with mock.patch("subprocess.run", side_effect=fake), \
                mock.patch("subprocess.Popen", return_value=forwarder) as popen, \
                mock.patch.object(deploy_release, "need"), \
                mock.patch.object(deploy_release, "port_free", return_value=True), \
                mock.patch.object(deploy_release, "wait_healthy"), \
                mock.patch.object(deploy_release, "issue_api_key",
                                  return_value="cs_kindkey123456"):
            deploy_release.deploy_kind(self.state, "test")
        kubeconfig = self.state / "kubeconfig"
        for argv, kw in calls:
            if argv[:2] in (["kind", "create"], ["kind", "load"]):
                self.assertEqual(kw["env"]["KUBECONFIG"], str(kubeconfig))
        self.assertEqual((self.state / "port-forward.pid").read_text().strip(), "4242")
        self.assertTrue((self.state / "deploy.log").exists())
        conn = json.loads((self.state / "connection.json").read_text())
        self.assertEqual(conn["base_url"], "http://127.0.0.1:18080")
        self.assertEqual(conn["api_key"], "cs_kindkey123456")
        # Generated secrets travel via env, never argv.
        creds = json.loads((self.state / "credentials.json").read_text())
        for argv, kw in calls:
            joined = " ".join(str(a) for a in argv)
            for value in creds.values():
                self.assertNotIn(value, joined)
        popen.assert_called_once()


class ComposeGuards(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.state = pathlib.Path(self.tmp.name) / "state"
        self.state.mkdir()

    def tearDown(self):
        self.tmp.cleanup()

    def _run_guard(self, fake):
        with mock.patch("subprocess.run", side_effect=fake), \
                mock.patch.object(deploy_release, "need"), \
                self.assertRaises(SystemExit):
            deploy_release.deploy_compose(self.state, "test")

    def test_existing_project_rejected(self):
        fake, _ = compose_fake(projects='[{"Name":"calcside-release-test"}]')
        self._run_guard(fake)
        self.assertFalse((self.state / "secrets.env").exists())

    def test_stopped_containers_rejected(self):
        fake, _ = compose_fake(containers="9f8e7d6c5b\n")
        self._run_guard(fake)
        self.assertFalse((self.state / "secrets.env").exists())

    def test_leftover_volume_rejected(self):
        fake, _ = compose_fake(volumes="calcside-release-test_postgres\n")
        self._run_guard(fake)
        self.assertFalse((self.state / "secrets.env").exists())

    def test_docker_listing_failure_fails_closed(self):
        fake, _ = compose_fake(ls_rc=1, ls_err="daemon off")
        self._run_guard(fake)

    def test_compose_flow_writes_state(self):
        fake, calls = compose_fake()
        with mock.patch("subprocess.run", side_effect=fake), \
                mock.patch.object(deploy_release, "need"), \
                mock.patch.object(deploy_release, "wait_healthy"), \
                mock.patch.object(deploy_release, "issue_api_key",
                                  return_value="cs_compkey123456"):
            deploy_release.deploy_compose(self.state, "test")
        conn = json.loads((self.state / "connection.json").read_text())
        self.assertEqual(conn["base_url"], "http://127.0.0.1:55001")
        self.assertEqual(conn["api_key"], "cs_compkey123456")
        backend = (self.state / "backend.env").read_text()
        self.assertIn("COMPOSE_PROJECT=calcside-release-test", backend)
        self.assertIn("BASE_URL=http://127.0.0.1:55001", backend)
        for name in ("connection.json", "credentials.json", "secrets.env",
                     "test.env", "backend.env"):
            mode = oct(os.stat(self.state / name).st_mode & 0o777)
            self.assertEqual(mode, "0o600", name)
        # compose up must have been invoked with the env file, secrets via env.
        up_calls = [argv for argv, _ in calls if "up" in argv]
        self.assertEqual(len(up_calls), 1)
        self.assertIn("--env-file", up_calls[0])
        creds = json.loads((self.state / "credentials.json").read_text())
        for argv, _ in calls:
            joined = " ".join(str(a) for a in argv)
            for value in creds.values():
                self.assertNotIn(value, joined)


class FilePermissions(unittest.TestCase):
    def test_write_private_enforces_600_on_preexisting(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = pathlib.Path(tmp) / "f"
            target.write_text("old")
            os.chmod(target, 0o644)
            deploy_release.write_private(target, "new")
            self.assertEqual(oct(os.stat(target).st_mode & 0o777), "0o600")
            self.assertEqual(target.read_text(), "new")

    def test_mask_only_on_github_actions(self):
        out = io.StringIO()
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}, clear=False), \
                redirect_stdout(out):
            deploy_release.mask("a-secret-value")
        self.assertIn("::add-mask::a-secret-value", out.getvalue())
        out = io.StringIO()
        env = dict(os.environ)
        env.pop("GITHUB_ACTIONS", None)
        with mock.patch.dict(os.environ, env, clear=True), redirect_stdout(out):
            deploy_release.mask("a-secret-value")
        self.assertEqual(out.getvalue(), "")


class DsnEncoding(unittest.TestCase):
    def test_values_dsn_url_encodes_credentials(self):
        cfg = {
            "pg_host": "postgres", "pg_port": 5432,
            "pg_user": "u@ser", "pg_db": "db/name",
            "pg_sslmode": "disable", "pg_password": "p@ss:wo/rd?x",
            "admin_user": "admin", "admin_password": "a",
            "worker_key": "w", "vault_key": "v",
            "console_origin": "http://localhost:8080",
            "api_replicas": 1, "persistence": False,
            "service_type": "ClusterIP", "image_tag": "test",
        }
        values = json.loads(deploy_test.gen_values(cfg))
        dsn = values["database"]["postgres"]["dsn"]
        self.assertTrue(dsn.startswith(
            "postgres://u%40ser:p%40ss%3Awo%2Frd%3Fx@postgres:5432/db%2Fname?"))
        self.assertIn("sslmode=disable", dsn)
        self.assertIn("search_path=public", dsn)


class DiagnosticsRedaction(unittest.TestCase):
    def test_terms_cover_all_state_secrets(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = pathlib.Path(tmp) / "state"
            state.mkdir()
            (state / "credentials.json").write_text(json.dumps(
                {"admin_password": "TOPSECRET123", "pg_password": "PGSECRET999"}))
            (state / "connection.json").write_text(json.dumps(
                {"base_url": "http://127.0.0.1:9", "api_key": "cs_KEYSECRET777"}))
            (state / "secrets.env").write_text("TEST_VAULT_KEY=VAULTSECRET888\n")
            terms = diag._redact_terms("compose", state)
            blob = b"admin TOPSECRET123 api cs_KEYSECRET777 vault VAULTSECRET888 pg PGSECRET999"
            redacted = diag._redact(blob, terms)
            for token in (b"TOPSECRET123", b"cs_KEYSECRET777",
                          b"VAULTSECRET888", b"PGSECRET999"):
                self.assertNotIn(token, redacted)
            self.assertEqual(redacted.count(b"<redacted>"), 4)

    def test_collect_outputs_no_secrets(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = pathlib.Path(tmp) / "state"
            state.mkdir()
            out = pathlib.Path(tmp) / "out"
            secret = "TOPSECRET12345"
            (state / "credentials.json").write_text(json.dumps(
                {"admin_password": secret}))
            (state / "connection.json").write_text(json.dumps(
                {"base_url": "http://127.0.0.1:9", "api_key": "cs_DIAGKEY99999"}))
            (state / "backend.env").write_text(
                "COMPOSE_PROJECT=calcside-release-test\n")

            canned = f"container logs {secret} and cs_DIAGKEY99999".encode()
            with mock.patch.object(diag, "_run", return_value=canned):
                diag.collect("compose", state, out)
            for path in out.iterdir():
                data = path.read_bytes()
                self.assertNotIn(secret.encode(), data, path.name)
                self.assertNotIn(b"cs_DIAGKEY99999", data, path.name)


if __name__ == "__main__":
    unittest.main(verbosity=2)
