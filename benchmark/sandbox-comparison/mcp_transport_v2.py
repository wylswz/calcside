#!/usr/bin/env python3
"""Native MCP (streamable HTTP, stateless) transport for one Calcside instance.

Stdlib only. Creates a short-lived instance-bound API key via the REST
control plane (csclient), performs the MCP initialize/tools handshake
against /mcp/instances/{iid}, and revokes exactly that key on close().

The key secret lives only in memory: it is never written to metadata,
logs, or error strings (all outbound error text is secret-redacted).
This module creates no instances and mutates no global auth state.
"""

import json
import urllib.error
import urllib.request

import csclient as cs

MCP_PROTOCOL_VERSION = "2025-03-26"
HTTP_TIMEOUT_S = 40
KEY_NAME = "bench-native-mcp"
KEY_TTL_S = 600


class NativeMCP:
    """JSON-RPC client for the instance-scoped MCP endpoint."""

    def __init__(self, iid):
        if not iid:
            raise ValueError("instance id required")
        self._iid = iid
        self._url = cs.BASE + "/mcp/instances/" + iid
        self._key_id = None
        self._secret = None
        self._session_id = None
        self._protocol_version = None
        self._next_id = 0
        self._create_key()

    # ---------------------------------------------------------- key lifecycle

    def _create_key(self):
        status, body = cs._req(
            "POST", "/api/v1/keys",
            {"name": KEY_NAME, "instance_id": self._iid,
             "expires_in_seconds": KEY_TTL_S},
            timeout=HTTP_TIMEOUT_S,
        )
        if status != 201 or not isinstance(body, dict):
            raise RuntimeError("key create failed: http %s %s"
                               % (status, str(body)[:300]))
        key = body.get("key") or {}
        self._key_id = key.get("id")
        self._secret = body.get("secret")
        if not self._key_id or not self._secret:
            if self._key_id:
                self._revoke_quietly()
            raise RuntimeError("key create response missing id/secret")

    def _revoke_quietly(self):
        try:
            cs._req("DELETE", "/api/v1/keys/" + self._key_id,
                    timeout=HTTP_TIMEOUT_S)
        except Exception:
            pass

    # ------------------------------------------------------------- transport

    def _sanitize(self, text):
        text = str(text)
        if self._secret:
            text = text.replace(self._secret, "[REDACTED]")
        return text

    def _headers(self):
        h = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "Authorization": "Bearer " + self._secret,
        }
        if self._protocol_version:
            h["MCP-Protocol-Version"] = self._protocol_version
        if self._session_id:
            h["Mcp-Session-Id"] = self._session_id
        return h

    @staticmethod
    def _sse_messages(body):
        messages = []
        data_lines = []

        def flush():
            if not data_lines:
                return
            payload = "\n".join(data_lines)
            data_lines.clear()
            try:
                messages.append(json.loads(payload))
            except json.JSONDecodeError as exc:
                raise RuntimeError("mcp SSE data not JSON: %s" % exc)

        for raw in body.decode("utf-8", "replace").splitlines():
            line = raw.rstrip("\r")
            if line.startswith("data:"):
                data_lines.append(line[5:].lstrip())
            elif line == "":
                flush()
        flush()
        return messages

    def _post(self, payload):
        req = urllib.request.Request(
            self._url, data=json.dumps(payload).encode(), method="POST",
            headers=self._headers())
        try:
            with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT_S) as r:
                status = r.status
                ctype = r.headers.get("Content-Type", "")
                sid = r.headers.get("Mcp-Session-Id")
                body = r.read()
        except urllib.error.HTTPError as exc:
            detail = exc.read()[:300]
            raise RuntimeError("mcp http %s: %s"
                               % (exc.code, self._sanitize(detail)))
        except Exception as exc:
            raise RuntimeError("mcp request failed: %s"
                               % self._sanitize(exc))
        if sid:
            self._session_id = sid
        if "text/event-stream" in ctype:
            return self._sse_messages(body)
        if not body:
            return []
        try:
            return [json.loads(body)]
        except json.JSONDecodeError:
            raise RuntimeError("mcp response not JSON: %s"
                               % self._sanitize(body[:200]))

    def _rpc(self, method, params=None, notify=False):
        msg = {"jsonrpc": "2.0", "method": method}
        rid = None
        if notify:
            if params is not None:
                msg["params"] = params
        else:
            rid = self._next_id
            self._next_id += 1
            msg["id"] = rid
            if params is not None:
                msg["params"] = params
        messages = self._post(msg)
        if notify:
            return None
        for m in messages:
            if isinstance(m, dict) and m.get("id") == rid:
                if "error" in m:
                    err = m.get("error") or {}
                    raise RuntimeError("mcp error %s: %s" % (
                        err.get("code"),
                        self._sanitize(err.get("message", err))))
                return m.get("result")
        raise RuntimeError("mcp: no response for id %s" % rid)

    # ------------------------------------------------------------- MCP verbs

    def initialize(self):
        """MCP initialize handshake; returns the original result object
        (protocolVersion, capabilities, serverInfo, instructions)."""
        result = self._rpc("initialize", {
            "protocolVersion": MCP_PROTOCOL_VERSION,
            "capabilities": {},
            "clientInfo": {"name": "sandbox-comparison", "version": "2"},
        })
        if not isinstance(result, dict):
            raise RuntimeError("mcp initialize returned non-object result")
        self._protocol_version = result.get("protocolVersion") \
            or MCP_PROTOCOL_VERSION
        self._rpc("notifications/initialized", notify=True)
        return result

    def list_tools(self):
        """All server tools, following nextCursor pagination."""
        tools = []
        cursor = None
        while True:
            params = {} if cursor is None else {"cursor": cursor}
            result = self._rpc("tools/list", params)
            if not isinstance(result, dict):
                raise RuntimeError("mcp tools/list returned non-object")
            tools.extend(result.get("tools") or [])
            cursor = result.get("nextCursor")
            if not cursor:
                return tools

    def call_tool(self, name, arguments):
        """tools/call with the caller's arguments verbatim; returns the
        original CallToolResult object (content, isError, ...)."""
        result = self._rpc("tools/call",
                           {"name": name, "arguments": arguments or {}})
        if not isinstance(result, dict):
            raise RuntimeError("mcp tools/call returned non-object result")
        return result

    def close(self):
        """Revoke only the key this instance created."""
        if self._secret is None:
            return
        key_id, self._key_id = self._key_id, None
        self._secret = None
        self._session_id = None
        if not key_id:
            return
        status, _ = cs._req("DELETE", "/api/v1/keys/" + key_id,
                            timeout=HTTP_TIMEOUT_S)
        if status >= 300 and status not in (404, 410):
            raise RuntimeError("key revoke failed: http %s" % status)


if __name__ == "__main__":
    raise SystemExit("library module; see smoke usage in report")
