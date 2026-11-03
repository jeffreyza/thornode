import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

SCRIPT = Path(__file__).resolve().parents[1] / "test-quality-coverage.py"
spec = importlib.util.spec_from_file_location("quality_coverage", SCRIPT)
coverage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(coverage)


class CoverageReportTest(unittest.TestCase):
    def test_end_to_end_changed_package_regression_report(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            subprocess.run(
                ["git", "config", "user.email", "test@example.com"],
                cwd=root,
                check=True,
            )
            subprocess.run(["git", "config", "user.name", "Test"], cwd=root, check=True)
            package = root / "pkg"
            package.mkdir()
            source = package / "a.go"
            source.write_text("package pkg\nfunc A() {}\n")
            subprocess.run(["git", "add", "."], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "base"], cwd=root, check=True)
            base = subprocess.check_output(
                ["git", "rev-parse", "HEAD"], cwd=root, text=True
            ).strip()
            source.write_text("package pkg\nfunc A() { println(1) }\n")
            subprocess.run(["git", "add", "."], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "change"], cwd=root, check=True)
            current = root / "current.txt"
            baseline = root / "baseline.txt"
            current.write_text("mode: set\nexample.com/mod/v3/pkg/a.go:2.1,2.24 1 0\n")
            baseline.write_text("mode: set\nexample.com/mod/v3/pkg/a.go:2.1,2.24 1 1\n")
            output = root / "report.json"
            subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--profile",
                    str(current),
                    "--baseline-profile",
                    str(baseline),
                    "--base-ref",
                    base,
                    "--output",
                    str(output),
                ],
                cwd=root,
                check=True,
                capture_output=True,
            )
            report = json.loads(output.read_text())
            self.assertEqual(report["changed_lines_by_package"], {"pkg": [1, 0]})
            self.assertEqual(
                report["regressions"]["pkg"], {"base": [1, 1], "current": [1, 0]}
            )

    def test_diff_hunks_identify_added_lines(self):
        diff = "+++ b/pkg/a.go\n@@ -3,0 +4,2 @@\n+one\n+two\n+++ b/pkg/b.go\n@@ -8 +8 @@\n+new\n"
        with patch.object(
            coverage.subprocess, "run", return_value=SimpleNamespace(stdout=diff)
        ):
            self.assertEqual(
                coverage.changed_lines("base"), {"pkg/a.go": {4, 5}, "pkg/b.go": {8}}
            )

    def test_changed_lines_and_baseline_regression(self):
        with tempfile.TemporaryDirectory() as tmp:
            current = Path(tmp) / "current.txt"
            baseline = Path(tmp) / "base.txt"
            current.write_text(
                "mode: set\ngitlab.com/thorchain/thornode/v3/pkg/a.go:5.1,8.1 2 0\n"
                "gitlab.com/thorchain/thornode/v3/pkg/a.pb.go:5.1,8.1 4 1\n"
            )
            baseline.write_text(
                "mode: set\ngitlab.com/thorchain/thornode/v3/pkg/a.go:5.1,8.1 2 1\n"
            )
            changed = {"pkg/a.go": {6}, "pkg/a.pb.go": {6}}
            self.assertEqual(
                coverage.summarize(coverage.profile(current), changed), {"pkg": [2, 0]}
            )
            self.assertEqual(
                coverage.summarize(coverage.profile(baseline), changed), {"pkg": [2, 2]}
            )
            self.assertEqual(
                coverage.package_summary(coverage.profile(current), {"pkg"}),
                {"pkg": [2, 0]},
            )


if __name__ == "__main__":
    unittest.main()
