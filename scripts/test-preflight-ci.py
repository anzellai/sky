#!/usr/bin/env python3
"""Public, offline release-admission contracts. No real API or tag push."""
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location("preflight_ci", Path(__file__).with_name("preflight-ci.py"))
ci = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci)


class Admission(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="preflight-contract-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.cmd("init", "--quiet")
        self.cmd("symbolic-ref", "HEAD", "refs/heads/main")
        self.cmd("remote", "add", "origin", "https://github.com/example/sky.git")
        (self.root / "input").write_text("public fixture\n")
        self.cmd("add", "input")
        self.tree = self.cmd("write-tree")
        self.head = self.commit(self.tree, "initial")
        self.cmd("update-ref", "refs/heads/main", self.head)
        self.state = ci.local_state(self.root, self.head)
        self.base = "repos/example/sky"
        self.rpath = self.base + "/actions/runs/5"
        self.run = {"id": 5, "repository": {"full_name": "example/sky"},
                    "event": "workflow_dispatch", "path": ci.WORKFLOW,
                    "workflow_id": 7, "head_sha": self.head, "run_attempt": 1,
                    "status": "completed", "conclusion": "success"}
        self.jobs = [{"id": i + 100, "run_id": 5, "run_attempt": 1,
                      "head_sha": self.head, "name": "gate-" + str(i),
                      "status": "completed", "conclusion": "success"} for i in range(50)]
        self.jobs += [{"id": i + 200, "run_id": 5, "run_attempt": 1,
                       "head_sha": self.head, "name": name,
                       "status": "completed", "conclusion": "skipped"}
                      for i, name in enumerate(["release", "docker"])]
        self.responses = {
            self.base + "/actions/workflows/release.yml": {"id": 7, "path": ci.WORKFLOW, "state": "active"},
            self.rpath: self.run,
            self.base + "/git/commits/" + self.head: {"sha": self.head, "tree": {"sha": self.tree}},
            self.base + "/git/ref/heads/main": {"ref": "refs/heads/main", "object": {"sha": self.head}},
            self.rpath + "/attempts/1": copy.deepcopy(self.run),
            self.rpath + "/attempts/1/jobs?per_page=100&page=1": {"total_count": 52, "jobs": self.jobs},
        }
        self.calls = []

    def cmd(self, *args):
        return ci.git(self.root, *args)

    def commit(self, tree, message):
        r = subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                            "-c", "commit.gpgsign=false", "commit-tree", tree], cwd=self.root,
                           input=message + "\n", text=True, capture_output=True, check=True)
        return r.stdout.strip()

    def fetch(self, endpoint):
        self.calls.append(endpoint)
        return copy.deepcopy(self.responses[endpoint])

    def verify(self):
        return ci.verify(self.fetch, self.state, 5)

    def refused(self):
        with self.assertRaises(ci.Refused):
            self.verify()
        self.assertFalse((self.root / ".git/last-preflight-pass").exists())

    def test_success_writes_truthful_atomic_stamp(self):
        proof = self.verify()
        stamp = ci.stamp(self.root, self.state, proof)
        record = json.loads(stamp.read_text())
        self.assertEqual(record["mode"], "verified-ci-release-run")
        self.assertEqual(record["successfulJobs"], 50)
        self.assertEqual(record["skippedPublishers"], ["docker", "release"])
        self.assertFalse(record["localTestsClaimed"])
        self.assertEqual(record["sourceTree"], self.tree)
        self.assertGreater(record["verifiedAtUnix"], 0)
        self.assertEqual(stamp.stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(stamp.parent.glob(".ci-preflight-*")), [])

    def test_branch_commit_same_tree_as_merged_main(self):
        producer = self.commit(self.tree, "branch spelling")
        self.run["head_sha"] = producer
        self.responses[self.rpath + "/attempts/1"]["head_sha"] = producer
        for j in self.jobs:
            j["head_sha"] = producer
        self.responses[self.base + "/git/commits/" + producer] = {"sha": producer, "tree": {"sha": self.tree}}
        self.assertNotEqual(producer, self.head)
        self.assertEqual(self.verify()["producerCommit"], producer)

    def test_wrong_tree(self):
        self.responses[self.base + "/git/commits/" + self.head]["tree"]["sha"] = "a" * 40
        self.refused()

    def test_wrong_repository(self):
        self.run["repository"]["full_name"] = "another/sky"
        self.refused()

    def test_wrong_workflow_path(self):
        self.run["path"] = ".github/workflows/rust-ci.yml"
        self.refused()

    def test_wrong_workflow_id(self):
        self.run["workflow_id"] = 8
        self.refused()

    def test_inactive_workflow(self):
        self.responses[self.base + "/actions/workflows/release.yml"]["state"] = "disabled_manually"
        self.refused()

    def test_unsafe_event(self):
        self.run["event"] = "pull_request_target"
        self.refused()

    def test_failed_or_incomplete_run(self):
        for state in ["failure", "cancelled", "timed_out", None]:
            with self.subTest(state=state):
                self.run["conclusion"] = state
                self.refused()

    def test_partial_job_set(self):
        self.responses[self.rpath + "/attempts/1/jobs?per_page=100&page=1"] = {"total_count": 51, "jobs": self.jobs[:-1]}
        self.refused()

    def test_unapproved_skip_or_failure(self):
        for state in ["skipped", "failure", "cancelled", "neutral", None]:
            with self.subTest(state=state):
                self.jobs[0]["conclusion"] = state
                self.refused()

    def test_in_progress_job(self):
        self.jobs[0]["status"] = "in_progress"
        self.refused()

    def test_publishers_must_skip(self):
        self.jobs[-1]["conclusion"] = "success"
        self.refused()

    def test_skip_name_cannot_impersonate_publisher(self):
        self.jobs[-1]["name"] = "other"
        self.refused()

    def test_wrong_job_attempt(self):
        self.jobs[0]["run_attempt"] = 2
        self.refused()

    def test_wrong_job_run_or_head(self):
        for field, value in [("run_id", 6), ("head_sha", "b" * 40)]:
            with self.subTest(field=field):
                old = self.jobs[0][field]
                self.jobs[0][field] = value
                self.refused()
                self.jobs[0][field] = old

    def test_duplicate_job_identity(self):
        self.jobs[1]["id"] = self.jobs[0]["id"]
        self.refused()

    def test_duplicate_job_name(self):
        self.jobs[1]["name"] = self.jobs[0]["name"]
        self.refused()

    def test_wrong_remote_main(self):
        self.responses[self.base + "/git/ref/heads/main"]["object"]["sha"] = "b" * 40
        self.refused()

    def test_dirty_and_untracked_refuse_before_stamp(self):
        for name in ["input", "untracked"]:
            with self.subTest(name=name):
                (self.root / name).write_text("changed")
                with self.assertRaises(ci.Refused):
                    ci.local_state(self.root, self.head)
                if name == "input":
                    self.cmd("checkout", "--", "input")
                else:
                    (self.root / name).unlink()

    def test_local_target_must_have_same_tree(self):
        (self.root / "input").write_text("different")
        self.cmd("add", "input")
        other = self.commit(self.cmd("write-tree"), "other tree")
        self.cmd("reset", "--hard", "HEAD")
        with self.assertRaises(ci.Refused):
            ci.local_state(self.root, other)

    def test_symlink_stamp_refused_without_touching_target(self):
        outside = self.root / ".git/untouched"
        outside.write_text("unchanged")
        (self.root / ".git/last-preflight-pass").symlink_to(outside)
        with self.assertRaises(ci.Refused):
            ci.stamp(self.root, self.state, self.verify())
        self.assertEqual(outside.read_text(), "unchanged")

    def test_dirty_after_verification_does_not_replace_old_stamp(self):
        old = self.root / ".git/last-preflight-pass"
        old.write_text("old verified record")
        proof = self.verify()
        (self.root / "input").write_text("changed")
        with self.assertRaises(ci.Refused):
            ci.stamp(self.root, self.state, proof)
        self.assertEqual(old.read_text(), "old verified record")

    def test_run_rerun_during_verification_refused(self):
        calls = 0
        def changing(endpoint):
            nonlocal calls
            result = self.fetch(endpoint)
            if endpoint == self.rpath:
                calls += 1
                if calls == 2:
                    result["run_attempt"] = 2
            return result
        with self.assertRaises(ci.Refused):
            ci.verify(changing, self.state, 5)

    def test_failed_only_rerun_preserves_successful_prior_jobs(self):
        prior = self.responses[self.rpath + "/attempts/1"]
        prior["conclusion"] = "failure"
        self.jobs[0]["conclusion"] = "failure"
        self.run["run_attempt"] = 2
        self.responses[self.rpath + "/attempts/2"] = copy.deepcopy(self.run)
        retry = {**self.jobs[0], "id": 999, "run_attempt": 2, "conclusion": "success"}
        self.responses[self.rpath + "/attempts/2/jobs?per_page=100&page=1"] = {"total_count": 1, "jobs": [retry]}
        proof = self.verify()
        self.assertEqual(proof["jobAttempts"]["gate-0"]["attempt"], 2)
        self.assertEqual(proof["jobAttempts"]["gate-1"]["attempt"], 1)

    def test_rerun_cannot_hide_prior_failed_job(self):
        self.responses[self.rpath + "/attempts/1"]["conclusion"] = "failure"
        self.jobs[0]["conclusion"] = "failure"
        self.run["run_attempt"] = 2
        self.responses[self.rpath + "/attempts/2"] = copy.deepcopy(self.run)
        self.responses[self.rpath + "/attempts/2/jobs?per_page=100&page=1"] = {"total_count": 0, "jobs": []}
        self.refused()

    def test_pagination_complete_and_truncated(self):
        chunks = {"jobs?per_page=2&page=1": {"total_count": 3, "jobs": [{"id": 1}, {"id": 2}]},
                  "jobs?per_page=2&page=2": {"total_count": 3, "jobs": [{"id": 3}]}}
        self.assertEqual(len(ci.pages(chunks.__getitem__, "jobs", page_size=2)), 3)
        chunks["jobs?per_page=2&page=2"]["jobs"] = []
        with self.assertRaises(ci.Refused):
            ci.pages(chunks.__getitem__, "jobs", page_size=2)

    def test_pagination_changed_count_or_duplicate(self):
        for second in [{"total_count": 4, "jobs": [{"id": 3}, {"id": 4}]},
                       {"total_count": 3, "jobs": [{"id": 2}]}]:
            with self.subTest(second=second):
                chunks = {"jobs?per_page=2&page=1": {"total_count": 3, "jobs": [{"id": 1}, {"id": 2}]},
                          "jobs?per_page=2&page=2": second}
                with self.assertRaises(ci.Refused):
                    ci.pages(chunks.__getitem__, "jobs", page_size=2)

    def test_origin_does_not_accept_credentials_or_another_host(self):
        for origin in ["https://token@github.com/example/sky.git", "https://elsewhere.invalid/example/sky.git", "example/sky"]:
            with self.subTest(origin=origin):
                with self.assertRaises(ci.Refused):
                    ci.repository(origin)
        self.assertEqual(ci.repository("git@github.com:example/sky.git"), "example/sky")



class ApiRequests(unittest.TestCase):
    """The GitHub request retries a timeout or a 5xx, refuses anything else at
    once, and its refusal names the request and the cause."""

    def fake(self, outcomes):
        calls = []

        def run(argv, cwd, timeout):
            calls.append(timeout)
            o = outcomes[len(calls) - 1]
            if isinstance(o, Exception):
                raise o
            return o
        return run, calls

    def test_timeout_then_answer(self):
        run, calls = self.fake([ci.CommandFailed("gh api x", "timeout", True), '{"ok": 1}'])
        self.assertEqual(ci.GitHub(Path("."), run)("repos/a/b/actions/runs/1/attempts/2/jobs"), {"ok": 1})
        self.assertEqual(calls, [45, 90])

    def test_server_error_then_answer(self):
        run, calls = self.fake([ci.CommandFailed("gh api x", "exit-1", True), '{"ok": 1}'])
        self.assertEqual(ci.GitHub(Path("."), run)("repos/a/b"), {"ok": 1})
        self.assertEqual(len(calls), 2)

    def test_client_error_refuses_without_retry(self):
        run, calls = self.fake([ci.CommandFailed("gh api repos/a/b", "exit-1", False)])
        with self.assertRaises(ci.Refused) as e:
            ci.GitHub(Path("."), run)("repos/a/b")
        self.assertEqual(len(calls), 1)
        self.assertIn("command-failed: gh api repos/a/b (exit-1)", str(e.exception))

    def test_retries_end(self):
        run, calls = self.fake([ci.CommandFailed("gh api x", "timeout", True)] * 3)
        with self.assertRaises(ci.Refused):
            ci.GitHub(Path("."), run)("repos/a/b")
        self.assertEqual(calls, [45, 90, 120])

    def test_real_timeout_and_exit_are_named(self):
        with self.assertRaises(ci.CommandFailed) as e:
            ci.command(["sleep", "5"], Path("."), timeout=0.2)
        self.assertEqual((e.exception.kind, e.exception.transient), ("timeout", True))
        with self.assertRaises(ci.CommandFailed) as e:
            ci.command(["sh", "-c", "echo 'gh: Server Error (HTTP 502)' >&2; exit 1"], Path("."))
        self.assertEqual((e.exception.kind, e.exception.transient), ("exit-1", True))
        with self.assertRaises(ci.CommandFailed) as e:
            ci.command(["sh", "-c", "echo 'gh: Not Found (HTTP 404)' >&2; exit 1"], Path("."))
        self.assertEqual((e.exception.kind, e.exception.transient), ("exit-1", False))
        self.assertNotIn("Not Found", str(e.exception))

if __name__ == "__main__":
    unittest.main()
