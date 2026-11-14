#!/usr/bin/env python3
"""Resume the recorded plan after a controller interruption, without overwrites."""

import hashlib
import json
import os
from datetime import datetime, timezone
from pathlib import Path

from evaluation_v2 import summarize
from run_v2 import run_trial, write_json


def main():
    os.environ["LANGSMITH_TRACING"] = "false"
    os.environ["LANGCHAIN_TRACING_V2"] = "false"
    here = Path(__file__).resolve().parent
    directory = here / "results" / "v2-agent"
    manifest = json.loads((directory / "manifest.json").read_text())
    assert "finished_at" not in manifest, "run is already complete"
    for name, expected in manifest["source_sha256"].items():
        assert hashlib.sha256((here / name).read_bytes()).hexdigest() == expected, name
    rows = json.loads((directory / "trials.json").read_text())
    assert 0 < len(rows) < len(manifest["order"])
    for index, row in enumerate(rows):
        expected = manifest["order"][index]
        assert [row["backend"], row["task"], row["repetition"]] == expected
        path = directory / ("%03d-%s-%s-%d.json" % (index, *expected))
        assert json.loads(path.read_text()) == row
    interrupted_at = len(rows)
    manifest.setdefault("interruptions", []).append({
        "reason": "interactive session interrupted the controller",
        "completed_trials_preserved": interrupted_at,
        "first_unrecorded_index": interrupted_at,
        "unrecorded_inflight_work": "unknown; may have incurred model/runtime work without a saved row",
        "resumed_at": datetime.now(timezone.utc).isoformat(),
        "resume_source_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
    })
    write_json(directory / "manifest.json", manifest)
    for index in range(interrupted_at, len(manifest["order"])):
        backend, task, repetition = manifest["order"][index]
        path = directory / ("%03d-%s-%s-%d.json" % (index, backend, task, repetition))
        assert not path.exists(), "refusing to overwrite trial"
        row = run_trial(backend, task, repetition, manifest["model"])
        rows.append(row)
        write_json(path, row)
        write_json(directory / "trials.json", rows)
        write_json(directory / "summary.json", summarize(rows))
        print("%d/%d %s %s r%d passed=%s" %
              (index + 1, len(manifest["order"]), backend, task, repetition, row["passed"]),
              flush=True)
    manifest["finished_at"] = datetime.now(timezone.utc).isoformat()
    write_json(directory / "manifest.json", manifest)


if __name__ == "__main__":
    main()
