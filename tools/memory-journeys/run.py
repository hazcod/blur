#!/usr/bin/env python3
"""Run feedback-guided personal-memory journeys and report observed counts."""

import argparse
import hashlib
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import time


def positive(value):
    count = int(value)
    if count < 1:
        raise argparse.ArgumentTypeError("must be positive")
    return count


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--executions", type=positive, default=1_000_000)
    parser.add_argument("--workers", type=positive, default=2)
    parser.add_argument("--minimize-seconds", type=positive, default=5,
                        help="maximum time per coverage/failure minimization attempt")
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    if args.workers > 8:
        parser.error("use at most eight workers")
    root = Path(__file__).resolve().parents[2]
    files = subprocess.check_output(["git", "ls-files", "-co", "--exclude-standard", "-z", "--",
                                     "*.go", "go.mod", "go.sum", "tools/memory-journeys/run.py",
                                     "internal/client/testdata/fuzz/*"], cwd=root).split(b"\0")
    source_hash = hashlib.sha256()
    for name in sorted(set(files) - {b""}):
        source_hash.update(name + b"\0")
        path = root / os.fsdecode(name)
        source_hash.update(path.read_bytes() if path.exists() else b"<deleted>")
    command = ["go", "test", "-p=2", "-timeout=0", "./internal/client", "-run", "^$",
               "-fuzz", "^FuzzPersonalMemoryJourney$",
               f"-fuzztime={args.executions}x", f"-parallel={args.workers}",
               f"-fuzzminimizetime={args.minimize_seconds}s"]
    start = time.monotonic()
    report = {"started_at": datetime.now(timezone.utc).isoformat(),
              "target_executions": args.executions, "workers": args.workers,
              "minimize_seconds": args.minimize_seconds, "command": command,
              "source_sha256": source_hash.hexdigest(),
              "reported_executions": 0, "new_interesting_inputs": 0,
              "status": "running",
              "claim": "Execution counts measure stress work, not distinct user needs or universal app support."}

    def save_report():
        if args.report:
            args.report.parent.mkdir(parents=True, exist_ok=True)
            args.report.write_text(json.dumps(report, indent=2) + "\n")

    print(json.dumps(report), flush=True)
    save_report()
    process = subprocess.Popen(command, cwd=root, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, text=True, start_new_session=True)
    try:
        for line in process.stdout:
            print(line, end="", flush=True)
            match = re.search(r"execs: (\d+).*new interesting: (\d+)", line)
            if match:
                report["reported_executions"] = int(match[1])
                report["new_interesting_inputs"] = int(match[2])
                save_report()
        code = process.wait()
    except KeyboardInterrupt:
        os.killpg(process.pid, signal.SIGINT)
        process.wait()
        code = 130
    report.update(exit_code=code, status="passed" if code == 0 else "incomplete_or_failed",
                  elapsed_seconds=round(time.monotonic() - start, 3))
    save_report()
    print(json.dumps(report), flush=True)
    return code


if __name__ == "__main__":
    raise SystemExit(main())
