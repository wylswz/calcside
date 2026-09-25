"""Prompt builder snapshot-style test."""

from __future__ import annotations

from calcside.langchain.prompt import SandboxInfo, SecretInfo, build_prompt

CATALOG_OPS = {
    "fs": [
        {"name": "read", "doc": "read a file", "params": ["path"]},
        {"name": "write", "doc": "write a file", "params": ["path", "content"]},
    ],
    "net": [
        {"name": "get", "doc": "HTTP GET", "params": ["url", "headers"]},
    ],
    "io": [{"name": "println", "doc": "print a line", "params": ["s"]}],
}


def _info() -> SandboxInfo:
    return SandboxInfo(
        instance_id="ins_abc",
        mode="per_run",
        capabilities={
            "fs": {"quota_bytes": 1024},
            "net": {"allow_hosts": ["api.example.com"], "methods": ["GET", "POST"]},
        },
        ops={k: v for k, v in CATALOG_OPS.items() if k != "io"},
        env={"API_HOST": "api.example.com"},
        secrets=[SecretInfo(name="TOKEN", allowed_domains=["api.example.com"])],
        limits={
            "exec_timeout_ms": 30000,
            "max_steps": 100000,
            "max_output_bytes": 4096,
        },
        ttl_seconds=900,
    )


def test_prompt_sections():
    p = build_prompt(_info())
    for needle in [
        "calcside sandbox",
        "ins_abc",
        "Starlark is NOT Python",
        "print(...)",
        "fs.read(path)",
        "net.get(url, headers)",
        "/work",
        "api.example.com",
        "{{secrets.NAME}}",
        "[REDACTED:NAME]",
        "`TOKEN`",
        'env.get("NAME")',
        "API_HOST",
        "policy_denied",
        "step_limit",
        "exec timeout 30000ms",
        "instance TTL 900s",
        "calcside_exec(code)",
        "calcside_list_files(path)",
        "calcside_read_file(path)",
    ]:
        assert needle in p, f"missing: {needle}"
    # ungranted capability must not be mentioned
    assert "io.println" not in p
    assert "#### `io`" not in p


def test_prompt_only_granted_caps():
    info = _info()
    info.capabilities = {"io": {}}
    info.ops = {"io": CATALOG_OPS["io"]}
    p = build_prompt(info)
    assert "io.println" in p
    assert "fs.read" not in p
    assert "net.get" not in p
    # file tools not advertised without fs
    assert "calcside_list_files" not in p
    assert "calcside_read_file" not in p
