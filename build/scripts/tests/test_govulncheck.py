"""Exit-status contract of scripts/govulncheck.sh, run against fixture scanner output.

The real script runs with a stub `go` first on PATH that prints a fixture instead of
running govulncheck, so no network or vulnerability database is needed.

Run with: python3 -m unittest discover -s build/scripts/tests -v
Requires bash and jq.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[3] / "scripts/govulncheck.sh"
GO_STUB = """#!/bin/sh
printf '%s\\n' "$@" > "$STUB_ARGS"
case "$2" in golang.org/x/vuln/cmd/govulncheck@*) ;; *) exit 97 ;; esac
[ -z "$STUB_OUTPUT" ] || cat "$STUB_OUTPUT"
exit "${STUB_RC:-0}"
"""
CONFIG = {"config": {"protocol_version": "v1.0.0", "scanner_name": "govulncheck"}}
PROGRESS = {"progress": {"message": "Scanning your code..."}}


def osv(osv_id, summary=None):
    return {"osv": {"id": osv_id, "summary": summary or f"summary of {osv_id}"}}


def finding(osv_id, depth=3, reachable=True, module="example.com/mod"):
    frames = [
        {
            "module": module,
            "version": "v1.0.0",
            "package": f"{module}/pkg{i}",
            "function": f"Func{i}",
        }
        for i in range(depth)
    ]
    frames[0]["receiver"] = "*Client"
    if not reachable:
        # module- and package-level findings carry no function in the innermost frame
        frames = [{"module": module, "version": "v1.0.0"}]
    return {"finding": {"osv": osv_id, "fixed_version": "v1.2.3", "trace": frames}}


def stream(*messages):
    return "".join(json.dumps(m, indent=2) + "\n" for m in messages)


class GovulncheckScriptTest(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory(prefix="govulncheck-")
        self.addCleanup(tmp.cleanup)
        self.dir = Path(tmp.name)
        bin_dir = self.dir / "bin"
        bin_dir.mkdir()
        go = bin_dir / "go"
        go.write_text(GO_STUB)
        go.chmod(0o755)
        self.output = self.dir / "scan.json"
        self.ignore = self.dir / "ignore"
        self.args = self.dir / "args"
        self.env = {
            "PATH": f"{bin_dir}{os.pathsep}{os.environ.get('PATH', os.defpath)}",
            "STUB_OUTPUT": str(self.output),
            "STUB_ARGS": str(self.args),
            "IGNORE_FILE": str(self.ignore),
        }

    def run_script(self, output, ignore="", rc=0, args=()):
        self.output.write_text(output)
        self.ignore.write_text(ignore)
        return subprocess.run(
            ["bash", str(SCRIPT), *args],
            env={**self.env, "STUB_RC": str(rc)},
            capture_output=True,
            text=True,
            timeout=60,
        )

    def assertExit(self, result, code):
        self.assertEqual(result.returncode, code, result.stdout + result.stderr)

    def assertHas(self, needle, text):
        if needle not in text:
            self.fail(f"{needle!r} not found in:\n{text}")

    def test_clean_scan_passes_and_scans_all_packages_with_mocknet(self):
        result = self.run_script(stream(CONFIG, PROGRESS))
        self.assertExit(result, 0)
        self.assertHas("0 reachable vulnerabilities, 0 allowlisted", result.stdout)
        self.assertHas(
            "no reachable vulnerabilities outside the allowlist", result.stdout
        )
        args = self.args.read_text().split()
        self.assertEqual(args[0], "run")
        self.assertEqual(args[2:], ["-tags", "mocknet", "-format", "json", "./..."])

    def test_explicit_packages_are_passed_through(self):
        result = self.run_script(stream(CONFIG), args=("./common/...",))
        self.assertExit(result, 0)
        self.assertEqual(self.args.read_text().split()[-1], "./common/...")

    def test_accepted_findings_pass(self):
        output = stream(
            CONFIG,
            osv("GO-2025-0001"),
            osv("GO-2025-0002"),
            finding("GO-2025-0001"),
            finding("GO-2025-0001", depth=5),
            finding("GO-2025-0002"),
            # not reachable, so neither reported nor required in the allowlist
            finding("GO-2025-0003", reachable=False),
        )
        ignore = "# comment\n\nGO-2025-0001  # reason\nGO-2025-0002 # reason\n"
        result = self.run_script(output, ignore)
        self.assertExit(result, 0)
        self.assertHas("2 reachable vulnerabilities, 2 allowlisted", result.stdout)
        self.assertNotIn("GO-2025-0003", result.stdout)
        self.assertNotIn("WARNING", result.stdout)

    def test_every_unaccepted_finding_is_reported(self):
        ids = ["GO-2026-0100", "GO-2026-0200", "GO-2026-0300"]
        messages = [CONFIG]
        for osv_id in ids:
            messages += [osv(osv_id), finding(osv_id, module=f"example.com/{osv_id}")]
        messages.append(finding("GO-2025-0001"))
        result = self.run_script(stream(*messages), "GO-2025-0001  # accepted\n")
        self.assertExit(result, 1)
        self.assertHas("3 reachable vulnerabilities not in", result.stdout)
        self.assertNotIn("GO-2025-0001:", result.stdout)
        for osv_id in ids:
            self.assertHas(f"{osv_id}: summary of {osv_id}", result.stdout)
            self.assertHas(f"https://pkg.go.dev/vuln/{osv_id}", result.stdout)
            self.assertHas(
                f"example.com/{osv_id}@v1.0.0 (fixed in v1.2.3)", result.stdout
            )
            # outermost caller first, innermost vulnerable symbol (with receiver) last
            self.assertHas(
                f"example.com/{osv_id}/pkg2.Func2\n"
                f"      -> example.com/{osv_id}/pkg1.Func1\n"
                f"      -> example.com/{osv_id}/pkg0.*Client.Func0",
                result.stdout,
            )

    def test_stale_entry_warns_without_failing(self):
        output = stream(CONFIG, osv("GO-2025-0001"), finding("GO-2025-0001"))
        result = self.run_script(
            output, "GO-2025-0001\nGO-2024-9999  # fixed upstream\n"
        )
        self.assertExit(result, 0)
        self.assertHas(
            "WARNING: GO-2024-9999 is allowlisted in "
            f"{self.ignore} but no longer reported; remove it",
            result.stdout,
        )

    def test_stale_entry_does_not_mask_an_unaccepted_finding(self):
        output = stream(CONFIG, osv("GO-2026-0100"), finding("GO-2026-0100"))
        result = self.run_script(output, "GO-2024-9999\n")
        self.assertExit(result, 1)
        self.assertHas("WARNING: GO-2024-9999", result.stdout)
        self.assertHas("GO-2026-0100: summary of GO-2026-0100", result.stdout)

    def test_scanner_failure_fails(self):
        # e.g. a build failure after some output was already written
        result = self.run_script(stream(CONFIG), rc=1)
        self.assertExit(result, 2)
        self.assertHas("scanner failed", result.stderr)

    def test_parser_failures_fail(self):
        cases = {
            "empty": "",
            "not json": "WARNING: sonic/ast only supports go1.17~1.26\n",
            "truncated": stream(CONFIG, osv("GO-2026-0100"))[:-40],
            "json without config": stream(PROGRESS),
            "text after valid json": stream(CONFIG) + "panic: boom\n",
            "finding is not an object": stream(CONFIG, {"finding": "GO-2026-0100"}),
            "finding without osv": stream(
                CONFIG, {"finding": {"trace": [{"function": "F"}]}}
            ),
        }
        for name, output in cases.items():
            with self.subTest(name):
                result = self.run_script(output)
                self.assertExit(result, 2)
                self.assertHas("ERROR", result.stderr)

    def test_allowlist_errors_fail(self):
        with self.subTest("malformed entry"):
            result = self.run_script(
                stream(CONFIG), "GO-2025-0001\nCVE-2025-1234  # wrong id kind\n"
            )
            self.assertExit(result, 2)
            self.assertHas("CVE-2025-1234", result.stderr)
        with self.subTest("missing jq"):
            # a bin dir holding only the go stub, bash and the coreutils the
            # script needs before its jq check
            bin_dir = self.dir / "nojq"
            bin_dir.mkdir()
            for tool in ("bash", "dirname", "sed", "awk", "grep", "mktemp", "rm"):
                path = shutil.which(tool)
                if path:
                    (bin_dir / tool).symlink_to(path)
            (bin_dir / "go").symlink_to(self.dir / "bin" / "go")
            result = subprocess.run(
                [shutil.which("bash"), str(SCRIPT)],
                env={**self.env, "PATH": str(bin_dir)},
                capture_output=True,
                text=True,
                timeout=60,
            )
            self.assertExit(result, 2)
            self.assertHas("jq is required", result.stderr)
        with self.subTest("missing file"):
            self.env["IGNORE_FILE"] = str(self.dir / "missing")
            result = self.run_script(stream(CONFIG))
            self.assertExit(result, 2)
            self.assertHas("cannot read allowlist", result.stderr)

    def test_large_trace_output_reports_every_id(self):
        # the first-sorted ID has far more output than a pipe buffer; the later ID
        # must still be reported
        messages = [CONFIG, osv("GO-2026-0001"), osv("GO-2026-0002")]
        messages += [finding("GO-2026-0001", depth=40) for _ in range(2000)]
        messages.append(finding("GO-2026-0002"))
        result = self.run_script(stream(*messages))
        self.assertExit(result, 1)
        self.assertHas("2 reachable vulnerabilities not in", result.stdout)
        self.assertHas("GO-2026-0001: summary of GO-2026-0001", result.stdout)
        self.assertHas("  ... and 1995 more traces", result.stdout)
        self.assertHas("GO-2026-0002: summary of GO-2026-0002", result.stdout)
        self.assertHas("Upgrade the affected dependency", result.stdout)


if __name__ == "__main__":
    unittest.main()
