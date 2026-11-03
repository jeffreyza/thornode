"""Exercise the DOGE mocknet entrypoint against stub dogecoind/dogecoin-cli binaries."""

import os
import signal
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "docker/mocknet/dogecoin-entrypoint.sh"

# The stub daemon runs until the stub CLI asks it to exit (a crash), or until
# SIGTERM arrives (a clean shutdown).
DOGECOIND_STUB = """#!/bin/sh
printf '%s\\n' "$@" > "$STATE/dogecoind-args"
trap 'touch "$STATE/dogecoind-clean-stop"; exit 0' TERM
while [ ! -e "$STATE/daemon-exit" ]; do sleep 0.02; done
exit 3
"""

# The stub CLI keeps the chain height in a file. It fails the next
# "fail-single" single-block generate calls with the EOF error seen in CI, and
# asks the daemon to exit after STOP_AFTER_BLOCKS successful single blocks.
CLI_STUB = """#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -*) echo "$1" >> "$STATE/cli-flags"; shift ;;
    *) break ;;
  esac
done
if [ -e "$STATE/daemon-exit" ]; then
  echo "error: couldn't connect to server" >&2
  exit 1
fi
height=$(cat "$STATE/height")
case "$1" in
  getblockcount)
    echo "$height"
    ;;
  generatetoaddress)
    echo "$2" >> "$STATE/generate-calls"
    if [ "$2" = 1 ]; then
      failures=$(cat "$STATE/fail-single" 2>/dev/null || echo 0)
      if [ "$failures" -gt 0 ]; then
        echo $((failures - 1)) > "$STATE/fail-single"
        echo "error: couldn't connect to server: EOF reached (code 1)" >&2
        exit 1
      fi
      singles=$(($(cat "$STATE/singles" 2>/dev/null || echo 0) + 1))
      echo "$singles" > "$STATE/singles"
      if [ "$singles" -ge "${STOP_AFTER_BLOCKS:-3}" ]; then
        touch "$STATE/daemon-exit"
      fi
    fi
    echo $((height + $2)) > "$STATE/height"
    echo '[]'
    ;;
  *)
    exit 2
    ;;
esac
"""


class DogecoinEntrypointTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="dogecoin-entrypoint-")
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.state = root / "state"
        self.state.mkdir()
        bin_dir = root / "bin"
        bin_dir.mkdir()
        for name, body in (("dogecoind", DOGECOIND_STUB), ("dogecoin-cli", CLI_STUB)):
            path = bin_dir / name
            path.write_text(body)
            path.chmod(0o755)
        self.env = {
            "PATH": f"{bin_dir}{os.pathsep}{os.defpath}",
            "STATE": str(self.state),
            "BLOCK_TIME": "0",
            "DOGE_STARTUP_RETRY_SECONDS": "0",
        }

    def set_height(self, height):
        (self.state / "height").write_text(f"{height}\n")

    def generate_calls(self):
        path = self.state / "generate-calls"
        return path.read_text().split() if path.exists() else []

    def assert_contains(self, needle, haystack):
        self.assertTrue(needle in haystack, f"{needle!r} not found in {haystack!r}")

    def run_entrypoint(self):
        return subprocess.run(
            ["sh", str(SCRIPT)],
            env=self.env,
            capture_output=True,
            text=True,
            timeout=20,
        )

    def test_fresh_chain_mines_initial_blocks_then_one_block_per_interval(self):
        self.set_height(0)
        result = self.run_entrypoint()

        # The daemon exited with status 3 and the entrypoint propagated it.
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertEqual(self.generate_calls(), ["1000", "1", "1", "1"])
        self.assert_contains("dogecoind exited with status 3", result.stderr)
        args = (self.state / "dogecoind-args").read_text().split()
        self.assert_contains("-rpcservertimeout=120", args)
        self.assert_contains("-rpcthreads=16", args)
        self.assert_contains(
            "-rpcclienttimeout=60", (self.state / "cli-flags").read_text()
        )

    def test_restart_keeps_existing_chain_without_mining_a_new_initial_batch(self):
        self.set_height(1500)
        result = self.run_entrypoint()

        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertEqual(self.generate_calls(), ["1", "1", "1"])
        self.assert_contains("initial blocks already mined", result.stderr)

    def test_partial_initial_mining_resumes_from_current_height(self):
        self.set_height(400)
        result = self.run_entrypoint()

        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertEqual(self.generate_calls(), ["600", "1", "1", "1"])

    def test_generate_failures_keep_the_daemon_running(self):
        self.set_height(1000)
        (self.state / "fail-single").write_text("2\n")
        result = self.run_entrypoint()

        # Both EOF failures are retried. The daemon only exits when the stub
        # asks it to after three successful blocks.
        self.assertEqual(result.returncode, 3, result.stderr)
        self.assertEqual(self.generate_calls(), ["1"] * 5)
        self.assert_contains("block generation failed (attempt 2)", result.stderr)
        self.assert_contains("recovered after 2 failed attempts", result.stderr)

    def test_sigterm_stops_dogecoind_cleanly(self):
        self.set_height(1000)
        proc = subprocess.Popen(
            ["sh", str(SCRIPT)],
            env={**self.env, "STOP_AFTER_BLOCKS": "1000000", "BLOCK_TIME": "0.05"},
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        self.addCleanup(proc.kill)
        deadline = time.monotonic() + 10
        while not self.generate_calls() and time.monotonic() < deadline:
            time.sleep(0.02)
        self.assertTrue(self.generate_calls(), "entrypoint never produced a block")

        proc.send_signal(signal.SIGTERM)
        _, stderr = proc.communicate(timeout=10)

        self.assertEqual(proc.returncode, 0, stderr)
        self.assertTrue((self.state / "dogecoind-clean-stop").exists())
        self.assert_contains("stopping dogecoind", stderr)


if __name__ == "__main__":
    unittest.main()
