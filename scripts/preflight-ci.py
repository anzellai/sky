#!/usr/bin/env python3
"""Authenticate a full, nonpublishing release run; never claim local tests ran.

The source tree, not the merge commit spelling, is the tested input. This admits
an unchanged branch tree merged to main. Every job's latest actual attempt must
pass, except the two tag-only publishers. No artifact or caller-supplied status
is accepted. Successful prior-attempt jobs survive a failed-only rerun.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time

WORKFLOW = ".github/workflows/release.yml"
PUBLISHERS = {"release", "docker"}
EXPECTED_SUCCESS = 50
MAX_ATTEMPTS = 20


class Refused(Exception):
    pass


def require(condition, label):
    if not condition:
        raise Refused(label)


def sha(value):
    require(type(value) is str and re.fullmatch(r"[0-9a-f]{40}", value), "invalid-object-id")
    return value


def positive(value):
    require(type(value) is int and value > 0, "invalid-positive-integer")
    return value


def command(argv, cwd):
    try:
        r = subprocess.run(argv, cwd=cwd, capture_output=True, timeout=45, check=True)
        return r.stdout.decode("utf-8").strip()
    except (OSError, subprocess.SubprocessError, UnicodeError):
        raise Refused("command-failed") from None


def git(root, *args):
    return command(["git", *args], root)


def repository(origin):
    # Never accept credential-bearing URLs, another forge or an inferred repo.
    m = re.fullmatch(r"(?:https://github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?", origin)
    require(m is not None, "unsupported-origin")
    return m[1]


def local_state(root, intended):
    require(git(root, "status", "--porcelain=v1", "--untracked-files=normal") == "", "dirty-worktree")
    head = sha(git(root, "rev-parse", "HEAD"))
    target = sha(git(root, "rev-parse", "--verify", intended + "^{commit}"))
    require(target == intended, "intended-commit-mismatch")
    tree = sha(git(root, "rev-parse", "HEAD^{tree}"))
    require(tree == sha(git(root, "rev-parse", intended + "^{tree}")), "local-tree-mismatch")
    return {"head": head, "commit": target, "tree": tree,
            "repo": repository(git(root, "remote", "get-url", "origin"))}


class GitHub:
    def __init__(self, root):
        self.root = root

    def __call__(self, endpoint):
        # gh owns authentication. Capture both streams: never print API errors,
        # credential context, response bodies or environment data.
        raw = command(["gh", "api", "--hostname", "github.com", endpoint], self.root)
        try:
            result = json.loads(raw)
        except (ValueError, TypeError):
            raise Refused("malformed-api-response") from None
        require(type(result) is dict, "api-object-required")
        return result


def pages(fetch, endpoint, page_size=100):
    rows, total, page = [], None, 1
    while total is None or len(rows) < total:
        d = fetch(f"{endpoint}?per_page={page_size}&page={page}")
        count = d.get("total_count")
        require(type(count) is int and 0 <= count <= 1000, "invalid-job-count")
        require(total is None or total == count, "pagination-count-changed")
        total = count
        chunk = d.get("jobs")
        require(type(chunk) is list and len(chunk) == min(page_size, total - len(rows)), "incomplete-job-page")
        rows.extend(chunk)
        page += 1
    require(len({positive(j.get("id")) for j in rows}) == len(rows), "duplicate-job-id")
    return rows


def run_identity(run, repo, run_id, workflow_id):
    require(run.get("id") == run_id and run.get("repository", {}).get("full_name") == repo,
            "wrong-run-repository")
    require(run.get("event") == "workflow_dispatch" and run.get("path") == WORKFLOW,
            "wrong-workflow-event")
    require(run.get("workflow_id") == workflow_id, "wrong-workflow-id")
    sha(run.get("head_sha"))


def verify(fetch, state, run_id):
    repo = state["repo"]
    base = f"repos/{repo}"
    workflow = fetch(base + "/actions/workflows/release.yml")
    workflow_id = positive(workflow.get("id"))
    require(workflow.get("path") == WORKFLOW and workflow.get("state") == "active", "wrong-workflow")
    run = fetch(f"{base}/actions/runs/{run_id}")
    run_identity(run, repo, run_id, workflow_id)
    require(run.get("status") == "completed" and run.get("conclusion") == "success", "run-not-successful")
    latest = positive(run.get("run_attempt"))
    require(latest <= MAX_ATTEMPTS, "too-many-attempts")
    producer = sha(run["head_sha"])
    commit = fetch(f"{base}/git/commits/{producer}")
    require(commit.get("sha") == producer and commit.get("tree", {}).get("sha") == state["tree"], "producer-tree-mismatch")
    main = fetch(base + "/git/ref/heads/main")
    require(main.get("ref") == "refs/heads/main" and main.get("object", {}).get("sha") == state["commit"], "remote-main-mismatch")
    chosen, seen_ids = {}, set()
    for attempt in range(1, latest + 1):
        prior = fetch(f"{base}/actions/runs/{run_id}/attempts/{attempt}")
        run_identity(prior, repo, run_id, workflow_id)
        require(prior.get("run_attempt") == attempt and prior.get("head_sha") == producer and prior.get("status") == "completed", "attempt-provenance-mismatch")
        if attempt == latest:
            require(prior.get("conclusion") == "success", "latest-attempt-not-successful")
        jobs = pages(fetch, f"{base}/actions/runs/{run_id}/attempts/{attempt}/jobs")
        names = set()
        for job in jobs:
            name = job.get("name")
            require(type(name) is str and name and name not in names, "duplicate-or-invalid-job-name")
            names.add(name)
            jid = positive(job.get("id"))
            require(jid not in seen_ids, "job-reused-across-attempts")
            seen_ids.add(jid)
            require(job.get("run_id") == run_id and job.get("run_attempt") == attempt and job.get("head_sha") == producer, "job-attempt-mismatch")
            chosen[name] = job
    require(len(chosen) == EXPECTED_SUCCESS + len(PUBLISHERS), "incomplete-job-set")
    for name, job in chosen.items():
        require(job.get("status") == "completed", "job-not-completed")
        require(job.get("conclusion") == ("skipped" if name in PUBLISHERS else "success"), "job-not-successful-or-authorized-skip")
    require(PUBLISHERS <= chosen.keys(), "publishers-missing")
    final = fetch(f"{base}/actions/runs/{run_id}")
    run_identity(final, repo, run_id, workflow_id)
    require(final.get("run_attempt") == latest and final.get("head_sha") == producer and final.get("status") == "completed" and final.get("conclusion") == "success", "run-changed-during-verification")
    main = fetch(base + "/git/ref/heads/main")
    require(main.get("ref") == "refs/heads/main" and main.get("object", {}).get("sha") == state["commit"], "remote-main-moved")
    return {"format": "sky-release-preflight-v1", "mode": "verified-ci-release-run",
            "repository": repo, "workflow": WORKFLOW, "runID": run_id,
            "runAttempt": latest, "producerCommit": producer, "intendedCommit": state["commit"],
            "verifiedHead": state["head"], "sourceTree": state["tree"],
            "successfulJobs": EXPECTED_SUCCESS, "skippedPublishers": sorted(PUBLISHERS),
            "jobAttempts": {n: {"id": j["id"], "attempt": j["run_attempt"]} for n, j in sorted(chosen.items())},
            "localTestsClaimed": False}


def stamp(root, state, proof):
    # Run all local checks again immediately before publication. No unchecked
    # stamp is ever written, and a preexisting symlink is never followed.
    require(local_state(root, state["commit"]) == state, "local-state-changed")
    common = Path(git(root, "rev-parse", "--git-common-dir"))
    if not common.is_absolute():
        common = root / common
    common = common.resolve(strict=True)
    destination = common / "last-preflight-pass"
    require(not destination.is_symlink() and (not destination.exists() or destination.is_file()), "unsafe-stamp-path")
    proof = {**proof, "verifiedAtUnix": int(time.time())}
    fd, temporary = tempfile.mkstemp(prefix=".ci-preflight-", dir=common)
    try:
        with os.fdopen(fd, "w") as stream:
            json.dump(proof, stream, sort_keys=True, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        require(not destination.is_symlink(), "unsafe-stamp-path")
        os.replace(temporary, destination)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ci-run", required=True, type=int)
    parser.add_argument("--commit", help="exact intended remote-main commit; defaults to HEAD")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    intended = sha(args.commit or git(root, "rev-parse", "HEAD"))
    state = local_state(root, intended)
    proof = verify(GitHub(root), state, positive(args.ci_run))
    stamp(root, state, proof)
    print("CI release preflight verified: 50 jobs passed; 2 publishers skipped; local tests not claimed.")


if __name__ == "__main__":
    try:
        main()
    except Refused as error:
        print("CI release preflight refused: " + str(error), file=sys.stderr)
        sys.exit(1)
