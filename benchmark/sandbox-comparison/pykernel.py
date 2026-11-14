#!/usr/bin/env python3
"""Long-lived Python kernel: the CodeAct baseline's execution surface.

Reads one JSON object per line on stdin ({"id": n, "code": "..."}) and writes
one JSON object per line on stdout. State persists in a single namespace,
which is exactly what an in-process interpreter gives an agent. Code's own
stdout/stderr are captured and returned in the reply, never mixed into the
protocol channel.
"""

import contextlib
import io
import json
import sys
import traceback

NS = {"__name__": "__agent__"}


def main():
    real = sys.stdout
    for line in sys.stdin:
        req = json.loads(line)
        buf = io.StringIO()
        err = None
        try:
            with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
                exec(compile(req["code"], "<agent>", "exec"), NS)
        except Exception:
            err = traceback.format_exc(limit=3)
        real.write(json.dumps({"id": req["id"], "out": buf.getvalue(), "error": err}) + "\n")
        real.flush()


if __name__ == "__main__":
    main()
