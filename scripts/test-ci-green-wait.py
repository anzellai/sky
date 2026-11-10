#!/usr/bin/env python3
"""Offline contracts for scripts/ci/wait-ci-green.py (the `decide` function)."""
import importlib.util
import pathlib
import unittest

path = pathlib.Path(__file__).parent / "ci" / "wait-ci-green.py"
spec = importlib.util.spec_from_file_location("wait_ci_green", path)
w = importlib.util.module_from_spec(spec)
spec.loader.exec_module(w)


def check(conclusion, started="2026-01-01T00:00:00Z"):
    return {"name": "ci-green", "conclusion": conclusion, "started_at": started}


class Decide(unittest.TestCase):
    def test_success(self):
        self.assertEqual(w.decide([check("success")], [], 0), "ok")

    def test_each_bad_conclusion_fails_at_once(self):
        for c in w.BAD:
            self.assertEqual(w.decide([check(c)], [], 0), "bad:" + c)

    def test_running_check_waits(self):
        self.assertEqual(w.decide([check(None)], [], 9999), "wait")

    def test_newest_check_wins(self):
        runs = [check("failure", "2026-01-01T00:00:00Z"), check("success", "2026-01-02T00:00:00Z")]
        self.assertEqual(w.decide(runs, [], 0), "ok")

    def test_other_check_names_are_ignored(self):
        other = {"name": "lint", "conclusion": "success", "started_at": "x"}
        self.assertEqual(w.decide([other], [], 0), "wait")

    def test_no_check_but_a_run_in_progress_waits_past_the_limit(self):
        for status in ("queued", "in_progress"):
            self.assertEqual(w.decide([], [{"status": status}], 9999), "wait")

    def test_no_check_and_no_run_waits_then_is_absent(self):
        self.assertEqual(w.decide([], [], 299), "wait")
        self.assertEqual(w.decide([], [], 300), "absent")

    def test_no_check_and_only_finished_runs_is_absent(self):
        self.assertEqual(w.decide([], [{"status": "completed"}], 300), "absent")


if __name__ == "__main__":
    unittest.main()
