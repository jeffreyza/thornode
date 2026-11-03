"""Check simulation coverage wiring without building or resetting a cluster."""

import os
import re
import shlex
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]


class SimulationConfigTest(unittest.TestCase):
    def render(self, *args, env=None):
        controlled_env = os.environ.copy()
        for key in (
            "TEST_SIMULATION_CLUSTER_STAGES",
            "TEST_SIMULATION_REQUIRE_XMR",
            "MAKEFLAGS",
            "MFLAGS",
            "MAKEOVERRIDES",
        ):
            controlled_env.pop(key, None)
        controlled_env.update(env or {})
        # Skip cluster prerequisites even in dry-run mode: these checks must
        # never build images, start containers, or reset developer volumes.
        result = subprocess.run(
            [
                "make",
                "--no-print-directory",
                "--dry-run",
                "CHECK_ENV=",
                "-o",
                "test-mocknet-startup",
                "-o",
                "test-xmr-readiness-config",
                "-o",
                "build-test-simulation",
                "-o",
                "build-mocknet-cluster",
                "-o",
                "build-mocknet-cluster-native",
                "-o",
                "reset-mocknet-cluster",
                "-o",
                "prepare-mocknet-cluster-native",
                *args,
            ],
            cwd=ROOT,
            env=controlled_env,
            capture_output=True,
            text=True,
            check=True,
            timeout=30,
        )
        return result.stdout

    def test_cluster_requires_xmr(self):
        command = self.render("test-simulation-cluster")
        self.assertRegex(command, r"-e XMR_POST_CHURN_REQUIRED=1\s")

    def test_cluster_includes_full_coverage(self):
        command = self.render("test-simulation-cluster")
        stages = re.search(r"STAGES=(\S+)", command).group(1).split(",")
        for stage in (
            "seed",
            "bootstrap",
            "swaps",
            "memoless-swaps",
            "xmr-spent-refs",
            "cluster-churn",
            "xmr-post-churn",
            "solvency",
            "ragnarok",
        ):
            with self.subTest(stage=stage):
                self.assertEqual(stages.count(stage), 1)

    def test_ci_cannot_inherit_an_optional_xmr_setting(self):
        ci = (ROOT / ".gitlab-ci.yml").read_text()
        job = re.search(r"(?ms)^test-simulation:\n(.*?)(?=^\S|\Z)", ci).group(1)
        commands = re.findall(
            r"(?m)^[ \t]+(?:-\s+)?(make test-simulation-cluster[^\n]*)$", job
        )
        self.assertEqual(len(commands), 1)
        rendered = self.render(
            *shlex.split(commands[0])[1:],
            env={"TEST_SIMULATION_REQUIRE_XMR": "0"},
        )
        self.assertRegex(rendered, r"-e XMR_POST_CHURN_REQUIRED=1\s")

    def test_local_opt_out_is_explicit(self):
        command = self.render(
            "test-simulation-cluster", "TEST_SIMULATION_REQUIRE_XMR=0"
        )
        self.assertRegex(command, r"-e XMR_POST_CHURN_REQUIRED=0\s")

    def test_readiness_target_still_requires_xmr(self):
        command = self.render(
            "test-simulation-xmr-readiness", "TEST_SIMULATION_REQUIRE_XMR=0"
        )
        self.assertRegex(command, r"-e XMR_POST_CHURN_REQUIRED=1\s")


if __name__ == "__main__":
    unittest.main()
