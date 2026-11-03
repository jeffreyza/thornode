"""Argument bounds of scripts/test-security-mutations.py.

--lock-wait 0 used to disarm the build-lock timer, so flock blocked forever;
negative values crashed in setitimer. Every duration flag must reject values
below one second before the runner touches the lock or runs go. The build lock
wait must also stop at the suite budget, even when --lock-wait is longer.

Run with: python3 -m unittest discover -s build/scripts/tests -v
"""

import fcntl
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[3] / "scripts/test-security-mutations.py"
FLAGS = ("--lock-wait", "--seconds-per-command", "--budget-seconds")


class DurationBoundsTest(unittest.TestCase):
    def run_script(self, *args):
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            capture_output=True,
            text=True,
            timeout=30,
        )

    def test_rejects_zero_and_negative_durations(self):
        for flag in FLAGS:
            for value in ("0", "-1"):
                with self.subTest(flag=flag, value=value):
                    result = self.run_script(flag, value)
                    self.assertEqual(result.returncode, 2, result.stderr)
                    self.assertTrue(
                        "must be at least 1" in result.stderr, result.stderr
                    )

    def test_rejects_non_integer_duration(self):
        result = self.run_script("--lock-wait", "soon")
        self.assertEqual(result.returncode, 2, result.stderr)


class LockBudgetTest(unittest.TestCase):
    def test_lock_wait_stops_at_suite_budget(self):
        with tempfile.TemporaryDirectory() as temp:
            lock_path = Path(temp) / "build.lock"
            summary = Path(temp) / "summary.json"
            with open(lock_path, "a+") as held:
                fcntl.flock(held, fcntl.LOCK_EX)
                started = time.monotonic()
                result = subprocess.run(
                    [
                        sys.executable,
                        str(SCRIPT),
                        "--budget-seconds",
                        "2",
                        "--lock-wait",
                        "600",
                        "--summary",
                        str(summary),
                    ],
                    capture_output=True,
                    text=True,
                    timeout=60,
                    env=dict(os.environ, THORNODE_TEST_QUALITY_LOCK=str(lock_path)),
                )
                elapsed = time.monotonic() - started
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertLess(elapsed, 30)
            data = json.loads(summary.read_text())
            self.assertEqual(data["result"], "baseline_failed")
            self.assertEqual(data["baseline"][0]["reason"], "build lock wait exceeded")


if __name__ == "__main__":
    unittest.main()
