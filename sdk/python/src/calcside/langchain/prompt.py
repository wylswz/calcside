"""System prompt generation for the calcside LangChain middleware.

The prompt is generated from the instance's actual granted capabilities
so the agent only ever hears about globals that exist.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class SecretInfo:
    name: str
    allowed_domains: list[str]


@dataclass
class SandboxInfo:
    """Everything the prompt builder needs to describe an instance."""

    instance_id: str
    mode: str  # "per_run" | "reused"
    tool_prefix: str = "calcside_"
    capabilities: dict[str, dict[str, Any]] = field(
        default_factory=dict
    )  # name -> spec config
    ops: dict[str, list[dict[str, Any]]] = field(
        default_factory=dict
    )  # name -> catalog ops
    env: dict[str, str] = field(default_factory=dict)
    secrets: list[SecretInfo] = field(default_factory=list)
    limits: dict[str, Any] = field(default_factory=dict)
    ttl_seconds: int | None = None

    def has(self, cap: str) -> bool:
        return cap in self.capabilities


def _op_sig(op: dict[str, Any]) -> str:
    params = ", ".join(op.get("params") or [])
    return f"{op.get('name')}({params})"


def build_prompt(info: SandboxInfo) -> str:
    p = info.tool_prefix
    lines: list[str] = [
        "## calcside sandbox",
        "",
        f"You have a persistent Starlark sandbox (instance {info.instance_id}). "
        f"Every `{p}exec` call runs code in the SAME instance: top-level variables and "
        "functions you define persist across calls"
        + (
            " and across agent runs while the instance lives."
            if info.mode == "reused"
            else "."
        ),
        "",
        "### Starlark is NOT Python",
        (
            "- No `import`, no classes, no `try/except`, no `with`, no"
            ' generators/`yield`, no f-strings (use `"%s" % x` or `.format()`).'
        ),
        (
            "- `while`, recursion, sets, dicts and list comprehensions are available;"
            " no `global` keyword is needed at top level."
        ),
        "- `json.encode(...)` / `json.decode(...)` and the `math` module are available.",
        (
            "- ALL output must go through `print(...)` — the value of the last"
            " expression is NOT returned."
        ),
        "",
        "### Tools",
        f"- `{p}exec(code)` — run Starlark code; returns printed output (or an error).",
    ]
    if info.has("fs"):
        lines += [
            f"- `{p}list_files(path)` — list a directory in the sandbox filesystem.",
            f"- `{p}read_file(path)` — read a file's content.",
        ]

    lines += ["", "### Capabilities"]
    for name, cfg in info.capabilities.items():
        lines.append(f"#### `{name}`")
        for op in info.ops.get(name, []):
            sig = _op_sig(op)
            doc = (op.get("doc") or "").strip()
            lines.append(f"- `{name}.{sig}`" + (f" — {doc}" if doc else ""))
        if name == "fs":
            lines.append("- Paths are rooted at `/work`.")
            if cfg.get("read_only"):
                lines.append("- The filesystem is read-only.")
            if cfg.get("quota_bytes"):
                lines.append(f"- Quota: {cfg['quota_bytes']} bytes.")
        if name == "net":
            hosts = cfg.get("allow_hosts") or []
            methods = cfg.get("methods") or []
            if hosts:
                lines.append(f"- Allowed hosts: {', '.join(hosts)}")
            if methods:
                lines.append(f"- Allowed methods: {', '.join(methods)}")
            lines.append(
                '- Responses are `{status, headers, body}` dicts (access via `r["body"]`).'
            )

    if info.secrets:
        lines += ["", "### Secrets"]
        lines.append(
            "Secret values are NEVER visible to you. Use the literal placeholder "
            "`{{secrets.NAME}}` inside net request URLs, header values, or bodies; the "
            "sandbox injects the real value for allowed domains and redacts it as "
            "`[REDACTED:NAME]` in responses. Never try to print or exfiltrate a secret."
        )
        for s in info.secrets:
            doms = ", ".join(s.allowed_domains) if s.allowed_domains else "(no domains)"
            lines.append(f"- `{s.name}` — allowed domains: {doms}")

    if info.env:
        lines += ["", "### Environment"]
        lines.append(
            'Read env vars in Starlark via `env.get("NAME")`; '
            "available keys: " + ", ".join(sorted(info.env))
        )

    lines += ["", "### Errors and limits"]
    lines += [
        (
            "- `policy_denied` — a security policy blocked the action. Do NOT retry"
            " the same action; explain to the user what was blocked."
        ),
        "- `step_limit` / `timeout` — simplify the code or split the work.",
        "- `syntax` / `runtime` / `out_of_scope` — fix the code and try again.",
    ]
    lim = info.limits
    bits = []
    if lim.get("exec_timeout_ms"):
        bits.append(f"exec timeout {lim['exec_timeout_ms']}ms")
    if lim.get("max_steps"):
        bits.append(f"{lim['max_steps']} max steps")
    if lim.get("max_output_bytes"):
        bits.append(f"output cap {lim['max_output_bytes']} bytes")
    if info.ttl_seconds:
        bits.append(f"instance TTL {info.ttl_seconds}s")
    if bits:
        lines.append("- Limits: " + ", ".join(bits) + ".")
    lines.append("- Keep outputs small: print summaries, not huge blobs.")

    lines += ["", "### Example"]
    if info.has("fs"):
        lines.append(
            f'`{p}exec`: `fs.write("out.txt", "hello")` then `print(fs.read("out.txt"))`'
        )
    if info.has("net"):
        host = (info.capabilities.get("net", {}).get("allow_hosts") or ["example.com"])[
            0
        ]
        lines.append(f'`{p}exec`: `print(net.get("https://{host}/")["body"])`')
    return "\n".join(lines)
