#!/usr/bin/env python3
"""Wait for the per-commit `ci-green` check of a commit, then exit 0 or 1.

Used by release.yml `verify-ci-green`. The decision is a pure function
(`decide`) so that scripts/test-ci-green-wait.py can pin every case offline.

Rules:
  success                                  exit 0
  failure, cancelled, timed_out,
  action_required, stale                   exit 1 at once
  no check run, but a queued or running
  rust-ci.yml run exists for the commit    keep waiting
  no check run and no such run for
  ABSENT_AFTER seconds                     exit 1 with "ci-green absent"
  still pending after the last attempt     exit 1
"""
import json
import subprocess
import sys
import time

BAD = ("failure", "cancelled", "timed_out", "action_required", "stale")
LIVE_RUN = ("queued", "in_progress", "waiting", "pending", "requested")


def latest_check(check_runs):
    """The newest `ci-green` check run, or None."""
    mine = [c for c in check_runs if c.get("name") == "ci-green"]
    if not mine:
        return None
    return sorted(mine, key=lambda c: c.get("started_at") or "")[-1]


def decide(check_runs, workflow_runs, elapsed, absent_after=300):
    """Return one of "ok", "bad:<conclusion>", "wait", "absent"."""
    check = latest_check(check_runs)
    if check is not None:
        conclusion = check.get("conclusion")
        if conclusion == "success":
            return "ok"
        if conclusion in BAD:
            return "bad:" + conclusion
        return "wait"
    if any(r.get("status") in LIVE_RUN for r in workflow_runs):
        return "wait"
    if elapsed >= absent_after:
        return "absent"
    return "wait"


def api(endpoint):
    out = subprocess.run(
        ["gh", "api", endpoint], capture_output=True, text=True, timeout=60
    )
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip() or "gh api failed")
    return json.loads(out.stdout)


def main(argv):
    repo, sha = argv[1], argv[2]
    attempts = int(argv[3]) if len(argv) > 3 else 90
    interval = int(argv[4]) if len(argv) > 4 else 30
    absent_after = int(argv[5]) if len(argv) > 5 else 300
    start = time.monotonic()
    for i in range(1, attempts + 1):
        try:
            # Filter by name in the query: the endpoint pages at 30 runs and a
            # release branch carries dozens, which once hid `ci-green` on page 2.
            checks = api(
                f"repos/{repo}/commits/{sha}/check-runs?check_name=ci-green&per_page=100"
            ).get("check_runs", [])
            runs = []
            if latest_check(checks) is None:
                runs = api(
                    f"repos/{repo}/actions/workflows/rust-ci.yml/runs?head_sha={sha}&per_page=100"
                ).get("workflow_runs", [])
            verdict = decide(checks, runs, time.monotonic() - start, absent_after)
        except Exception as err:  # a failed read is not a verdict, ask again
            print(f"attempt {i}: read failed ({err}), asking again")
            time.sleep(interval)
            continue
        print(f"attempt {i}: {verdict}")
        if verdict == "ok":
            print(f"ci-green is SUCCESS for {sha}. Safe to release.")
            return 0
        if verdict.startswith("bad:"):
            print(
                f"::error::ci-green is '{verdict[4:]}' for {sha}. Refusing to release "
                "an unverified commit. Fix ci-green on main first.",
                file=sys.stderr,
            )
            return 1
        if verdict == "absent":
            print(
                f"::error::ci-green absent for {sha}: no check run and no queued or "
                f"running rust-ci.yml run after {absent_after} s. Refusing to release.",
                file=sys.stderr,
            )
            return 1
        time.sleep(interval)
    print(
        f"::error::ci-green did not reach a conclusion for {sha} within the wait "
        "window. Refusing to release.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
