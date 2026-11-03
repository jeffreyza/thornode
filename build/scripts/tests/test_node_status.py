"""Run the real node-status script against canned endpoint responses."""

import json
import os
import re
import subprocess
import tempfile
import time
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "node-status.sh"
REF = "http://peer"
THORNODE_RPC = "thornode:27147"
SCANNER = "http://bifrost:6040/status/scanner"
API = "http://thornode:1317/thorchain"

# Every curl the script issues ends with its URL, which is logged. The body for
# a URL is the fixture file named after it; a URL with no fixture fails as curl
# --fail would. A fixture may carry an exit status beside it, for a body that
# was cut off partway.
CURL_STUB = """#!/bin/sh
for arg; do url=$arg; done
printf '%s\\n' "$url" >>"$FIXTURES.log"
file="$FIXTURES/$(printf '%s' "$url" | tr -c 'A-Za-z0-9' '_')"
[ -f "$file" ] || exit 22
cat "$file"
[ -f "$file.rc" ] && exit "$(cat "$file.rc")"
exit 0
"""
THORNODE_STUB = """#!/bin/sh
echo thor1test
"""

CONSTANTS = {
    "int_64_values": {
        "ChurnMigrateRounds": 5,
        "FundMigrationInterval": 10,
        "ChurnInterval": 300,
        "ChurnRetryInterval": 720,
        "BlocksPerYear": 5256000,
    }
}


def status(height, catching_up=False):
    return {
        "result": {
            "sync_info": {"latest_block_height": height, "catching_up": catching_up}
        }
    }


class NodeStatusTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="node-status-")
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.fixtures = root / "fixtures"
        self.fixtures.mkdir()
        bin_dir = root / "bin"
        bin_dir.mkdir()
        for name, stub in (("curl", CURL_STUB), ("thornode", THORNODE_STUB)):
            path = bin_dir / name
            path.write_text(stub)
            path.chmod(0o755)
        self.env = {
            "PATH": f"{bin_dir}:{os.environ.get('PATH', os.defpath)}",
            "FIXTURES": str(self.fixtures),
            "VALIDATOR": "false",
            "PEER": REF,
            "NET": "",
        }
        self.serve(f"{THORNODE_RPC}/status", status(500))
        self.serve(f"{REF}:27147/status", status(500))
        self.serve("http://thornode:1317/thorchain/node/thor1test", {})
        self.serve("http://thornode:1317/thorchain/mimir", {})
        self.serve("http://thornode:1317/thorchain/constants", CONSTANTS)
        self.vaults(400)

    def serve(self, url, body, rc=None):
        name = re.sub(r"[^A-Za-z0-9]", "_", url)
        text = body if isinstance(body, str) else json.dumps(body)
        (self.fixtures / name).write_text(text)
        if rc is not None:
            (self.fixtures / f"{name}.rc").write_text(str(rc))

    def drop(self, url):
        (self.fixtures / re.sub(r"[^A-Za-z0-9]", "_", url)).unlink()

    def requested(self):
        log = Path(f"{self.fixtures}.log")
        return log.read_text().splitlines() if log.exists() else []

    def forget_requests(self):
        Path(f"{self.fixtures}.log").unlink(missing_ok=True)

    def vaults(self, height, status="ActiveVault", membership=()):
        self.serve(
            f"{API}/vaults/asgard",
            [
                {
                    "block_height": height,
                    "status": status,
                    "membership": list(membership),
                    "addresses": [{"chain": "THOR", "address": "thor1vault"}],
                }
            ],
        )

    def run_script(self, **env):
        result = subprocess.run(
            ["bash", str(SCRIPT)],
            env={**self.env, **env},
            capture_output=True,
            text=True,
            timeout=60,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stderr, "")
        return result.stdout

    def row(self, out, chain):
        # The label column is 18 wide, and "ETH (beacon slot)" has spaces in it.
        for line in out.splitlines():
            if line[:18].strip() == chain:
                return [chain] + line[18:].split()
        self.fail(f"no {chain} row in:\n{out}")

    def has_row(self, out, chain):
        return any(line[:18].strip() == chain for line in out.splitlines())

    def field(self, out, label):
        for line in out.splitlines():
            if line.strip().startswith(label + ":"):
                return line.split(":", 1)[1].strip()
        return None

    def test_reference_at_local_height_is_in_sync(self):
        out = self.run_script()
        self.assertEqual(
            self.row(out, "THOR"), ["THOR", "-", "-", "100.000%", "0", "500"]
        )
        self.assertEqual(self.field(out, "Churn Interval"), "300")

    def test_missing_reference_is_an_error_not_in_sync(self):
        self.drop(f"{REF}:27147/status")
        for case, peer in (("down", REF), ("unconfigured", "")):
            with self.subTest(case=case):
                out = self.run_script(PEER=peer)
                self.assertEqual(
                    self.row(out, "THOR"), ["THOR", "-", "-", "Error", "-", "500"]
                )
                # A node that reports itself caught up still gets estimates.
                self.assertEqual(self.field(out, "Churn Interval"), "300")

    def test_missing_reference_while_catching_up_skips_estimates(self):
        self.drop(f"{REF}:27147/status")
        self.serve(f"{THORNODE_RPC}/status", status(500, catching_up=True))
        out = self.run_script()
        self.assertEqual(self.row(out, "THOR"), ["THOR", "-", "-", "Error", "-", "500"])
        self.assertNotIn("CHURN ESTIMATES", out)

    def test_catching_up_is_never_reported_in_sync(self):
        for ref in (500, 450):
            with self.subTest(ref=ref):
                self.serve(f"{THORNODE_RPC}/status", status(500, catching_up=True))
                self.serve(f"{REF}:27147/status", status(ref))
                sync = self.row(self.run_script(), "THOR")[3]
                self.assertEqual(sync, "syncing")

    def test_non_numeric_height_is_missing_not_zero(self):
        self.serve(f"{THORNODE_RPC}/status", status("abc"))
        out = self.run_script()
        self.assertEqual(self.row(out, "THOR"), ["THOR", "-", "-", "Error", "-", "500"])

    def test_validator_without_bifrost_answer_is_an_error(self):
        out = self.run_script(VALIDATOR="true")
        self.assertEqual(self.row(out, "BTC")[1], "Error")

    def test_churn_between_local_and_reference_height_is_overdue(self):
        # Next churn at 400 + 300 = 700: past the reference tip (720), not yet
        # reached locally (690). Both are measured from the reference. Halted,
        # so the keygen window does not apply.
        self.serve(f"{API}/mimir", {"HALTCHURNING": 1})
        self.serve(f"{THORNODE_RPC}/status", status(690))
        self.serve(f"{REF}:27147/status", status(720))
        out = self.run_script()
        self.assertTrue(self.field(out, "ETA Churn Start").startswith("overdue by 20"))
        self.assertTrue(self.field(out, "ETA Churn Finish").startswith("n/a"))

    def test_overdue_churn_has_no_finish_estimate(self):
        # Next churn at 700, overdue at 710 while its nominal finish (761) is
        # still ahead.
        self.serve(f"{API}/mimir", {"HALTCHURNING": 1})
        self.serve(f"{THORNODE_RPC}/status", status(710))
        self.serve(f"{REF}:27147/status", status(710))
        out = self.run_script()
        self.assertTrue(self.field(out, "ETA Churn Start").startswith("overdue by 10"))
        self.assertTrue(self.field(out, "ETA Churn Finish").startswith("n/a"))

    def test_keygen_window_after_churn_height_is_in_progress(self):
        # Churn at 700 and every 720 blocks after it until a keygen succeeds.
        # The keygen window is ten minutes: 100 blocks at the nominal rate.
        for height, start in ((710, 700), (1430, 1420)):
            with self.subTest(height=height):
                self.serve(f"{THORNODE_RPC}/status", status(height))
                self.serve(f"{REF}:27147/status", status(height))
                out = self.run_script()
                self.assertEqual(
                    self.field(out, "ETA Churn Start"),
                    "keygen in progress (started 10 blocks ago)",
                )
                self.assertEqual(self.field(out, "Start Height"), str(start))
                # The migration counts from the attempt, 61 blocks after it.
                self.assertEqual(self.field(out, "Finish Height"), str(start + 61))
                self.assertFalse(self.field(out, "ETA Churn Finish").startswith("n/a"))

    def test_churn_past_keygen_window_is_overdue(self):
        self.serve(f"{THORNODE_RPC}/status", status(900))
        self.serve(f"{REF}:27147/status", status(900))
        out = self.run_script()
        self.assertTrue(self.field(out, "ETA Churn Start").startswith("overdue by 200"))
        self.assertEqual(self.field(out, "Start Height"), "700")

    def test_membership_shown_without_mimir_or_constants(self):
        self.drop(f"{API}/mimir")
        self.drop(f"{API}/constants")
        self.serve(f"{API}/node/thor1test", {"pub_key_set": {"secp256k1": "pk1"}})
        self.vaults(400, membership=["pk1"])
        out = self.run_script()
        self.assertNotIn("CHURN ESTIMATES", out)
        self.assertEqual(
            self.field(out, "Active Member"), "https://thorchain.net/address/thor1vault"
        )
        self.assertEqual(self.field(out, "Unbond Allowed"), "false")

    def test_membership_hidden_without_vaults(self):
        # An empty vault list has no memberships in it, which is not the same
        # as being in none of them.
        self.serve(f"{API}/node/thor1test", {"pub_key_set": {"secp256k1": "pk1"}})
        self.drop(f"{API}/vaults/asgard")
        self.assertNotIn("Unbond Allowed", self.run_script())

    def test_seed_fallback_skips_seeds_without_a_height(self):
        good = "http://seed2:27147/status"
        self.serve(good, status(500))
        cases = {
            # Cut off by the timeout after part of the body arrived.
            "truncated": ('{"result": {"sync_', 28),
            # Answered 200 with something that is not a status.
            "error page": ("<html>bad gateway</html>", None),
        }
        for case, (body, rc) in cases.items():
            with self.subTest(case=case):
                self.serve("http://seed1:27147/status", body, rc)
                out = self.run_script(PEER="", SEEDS="http://seed1,http://seed2")
                self.assertEqual(
                    self.row(out, "THOR"), ["THOR", "-", "-", "100.000%", "0", "500"]
                )

    def validator(self, **env):
        return self.run_script(VALIDATOR="true", **env)

    def test_scanner_states(self):
        scanner = {
            "BTC": {"block_scanner_height": 90, "chain_height": 100, "healthy": True},
            # One block ahead of the tip bifrost happened to read: a diff of
            # -1, which is not bifrost's could-not-read sentinel.
            "LTC": {"block_scanner_height": 101, "chain_height": 100, "healthy": True},
            # The sentinel itself, on a height.
            "BCH": {"block_scanner_height": -1, "chain_height": 100, "healthy": True},
            "DOGE": {"block_scanner_height": 95, "chain_height": 100, "healthy": False},
        }
        self.serve(SCANNER, scanner)
        out = self.validator()
        self.assertEqual(self.row(out, "BTC")[1:3], ["90.000%", "-10"])
        self.assertEqual(self.row(out, "LTC")[2], "1")
        self.assertEqual(self.row(out, "BCH")[1], "Error")
        self.assertEqual(self.row(out, "DOGE")[1:3], ["95.000%", "-5*"])
        self.assertIn("* bifrost scanner unhealthy", out)  # codespell:ignore
        # Enabled, but bifrost holds no client for it.
        self.assertEqual(self.row(out, "GAIA")[1], "missing")
        # No scanner height for THORChain, whatever bifrost says.
        self.assertEqual(self.row(out, "THOR")[1:3], ["-", "-"])

    def test_unreadable_scanner_body_is_an_error_not_missing(self):
        for case, body in (
            ("error page", "<html>bad gateway</html>"),
            ("truncated", '{"BTC": {"block_scanner_'),
            ("not an object", "[]"),
        ):
            with self.subTest(case=case):
                self.serve(SCANNER, body)
                out = self.validator()
                self.assertEqual(self.row(out, "BTC")[1], "Error")
                self.assertNotIn("missing", out)

    def test_jsonrpc_error_body_is_not_a_height(self):
        url = "http://thorchain:password@bitcoin-daemon:8332"
        self.serve(url, {"result": None, "error": {"code": -32005}, "id": "x"})
        self.assertEqual(self.row(self.validator(), "BTC")[3], "Error")

    def test_utxo_url_with_scheme_is_used_as_given(self):
        info = {"result": {"blocks": 100, "headers": 100, "verificationprogress": 1}}
        self.serve("https://btc.example/key", info)
        out = self.validator(BIFROST_CHAINS_BTC_RPC_HOST="https://btc.example/key")
        self.assertEqual(self.row(out, "BTC")[3:], ["100.000%", "0", "100"])

    def test_disabled_chain_is_neither_probed_nor_shown(self):
        out = self.validator(BIFROST_CHAINS_BSC_DISABLED="true")
        self.assertFalse(self.has_row(out, "BSC"))
        self.assertTrue(self.has_row(out, "BASE"))
        requested = self.requested()
        self.assertNotIn("https://bsc.nodereal.io", requested)
        self.assertNotIn("http://binance-smart-daemon:8545", requested)
        self.assertIn("https://mainnet.base.org", requested)  # codespell:ignore

    def test_beacon_endpoint(self):
        syncing = {"data": {"head_slot": "100", "sync_distance": "0"}}
        cases = (
            # host, beacon url it implies, cell when it answers, when it does not
            (
                "http://ethereum-daemon:8545",
                "http://ethereum-daemon:3500",
                "100.000%",
                "Error",
            ),
            ("http://my-eth:8545", "http://my-eth:3500", "100.000%", "-"),
            ("https://eth.example/v2/key", None, "-", "-"),
        )
        for host, beacon, up, down in cases:
            for answering in (True, False):
                with self.subTest(host=host, answering=answering):
                    self.forget_requests()
                    if beacon and answering:
                        self.serve(f"{beacon}/eth/v1/node/syncing", syncing)
                    out = self.validator(BIFROST_CHAINS_ETH_RPC_HOST=host)
                    cell = self.row(out, "ETH (beacon slot)")[3]
                    self.assertEqual(cell, up if answering else down)
                    if beacon is None:
                        self.assertFalse(
                            any(
                                u.endswith("/eth/v1/node/syncing")
                                for u in self.requested()
                            )
                        )
                    if beacon and answering:
                        self.drop(f"{beacon}/eth/v1/node/syncing")

    def test_named_beacon_that_does_not_answer_is_an_error(self):
        out = self.validator(
            BIFROST_CHAINS_ETH_RPC_HOST="https://eth.example/v2/key",
            ETH_BEACON_HOST="http://beacon:5052",
        )
        self.assertEqual(self.row(out, "ETH (beacon slot)")[3], "Error")

    def test_implausible_block_time_falls_back_to_nominal_rate(self):
        # Next churn at 700 is 200 blocks from 500; the mirror block 300 claims
        # a time an hour in the future. Nominally 200 blocks are 20 minutes; the
        # negative measurement wrapped to "0d 23h 20m".
        future = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(time.time() + 3600))
        self.serve(
            f"http://{THORNODE_RPC}/block?height=300",
            {"result": {"block": {"header": {"time": future + ".0Z"}}}},
        )
        out = self.run_script()
        self.assertTrue(self.field(out, "ETA Churn Start").startswith("0d 0h 20m"))

    def test_empty_mimir_falls_back_to_constants(self):
        self.serve("http://thornode:1317/thorchain/mimir", "")
        out = self.run_script()
        self.assertEqual(self.field(out, "Churn Interval"), "300")
        self.assertEqual(self.field(out, "Migration Interval"), "10")
        self.assertEqual(self.field(out, "Start Height"), "700")
        self.assertNotIn("HALTCHURNING", out)

    def test_no_constants_or_mimir_skips_estimates(self):
        self.drop("http://thornode:1317/thorchain/mimir")
        self.drop("http://thornode:1317/thorchain/constants")
        out = self.run_script()
        self.assertNotIn("CHURN ESTIMATES", out)


if __name__ == "__main__":
    unittest.main()
