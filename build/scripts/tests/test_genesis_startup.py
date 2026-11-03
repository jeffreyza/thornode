"""Exercise the genesis entrypoint across fresh boots and container restarts."""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "genesis.sh"

CORE = """#!/bin/bash
resolve_signer_seed_phrase() { :; }
init_chain() {
  echo init >> "$HOME/events"
  if [ "$TEST_INIT" = interrupted ]; then
    kill -KILL "$$"
  fi
  mkdir -p "$HOME/.thornode/config"
  echo '{}' > "$HOME/.thornode/config/genesis.json"
}
create_thor_user() { :; }
fetch_version() { echo 1.0.0; }
add_node_account() { :; }
disable_bank_send() { :; }
add_account() { :; }
add_account_if_missing() { :; }
reserve() { :; }
set_bond_module() { :; }
"""

UNSAFE = """#!/bin/bash
deploy_evm_contracts() {
  echo deploy >> "$HOME/events"
  # A successful chain deployment can precede another chain's failure.
  echo deployed > "$HOME/eth-contract"
  if [ "$TEST_DEPLOY" = interrupted ]; then
    kill -KILL "$$"
  fi
  [ "$TEST_DEPLOY" = success ]
}
"""

THORNODE = """#!/bin/bash
case "$1" in
  pubkey|ed25519)
    cat >/dev/null
    echo key
    ;;
  keys|tendermint) echo key ;;
  genesis)
    echo validate >> "$HOME/events"
    exit "$TEST_VALIDATE_EXIT"
    ;;
  render-config|start) echo "$1" >> "$HOME/events" ;;
  *) exit 1 ;;
esac
"""


class GenesisStartupTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="mocknet-genesis-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        # A space in HOME also exercises marker path quoting.
        self.home = self.root / "node home"
        self.home.mkdir()
        self.scripts = self.root / "scripts"
        self.scripts.mkdir()
        shutil.copyfile(SCRIPT, self.scripts / "genesis.sh")
        (self.scripts / "core.sh").write_text(CORE)
        (self.scripts / "core-unsafe.sh").write_text(UNSAFE)
        binary = self.scripts / "thornode"
        binary.write_text(THORNODE)
        binary.chmod(0o755)
        self.env = {
            "HOME": str(self.home),
            "PATH": str(self.scripts) + os.pathsep + os.defpath,
            "NET": "mocknet",
            "SIGNER_NAME": "test",
            "SIGNER_PASSWD": "test",
            "SIGNER_SEED_PHRASE": "test",
            "EXTERNAL_IP": "127.0.0.1",
            "TEST_DEPLOY": "success",
            "TEST_VALIDATE_EXIT": "0",
        }
        self.genesis = self.home / ".thornode/config/genesis.json"

    def run_genesis(self, **overrides):
        return subprocess.run(
            ["bash", str(self.scripts / "genesis.sh")],
            env={**self.env, **overrides},
            text=True,
            capture_output=True,
            timeout=10,
        )

    def events(self):
        path = self.home / "events"
        return path.read_text().splitlines() if path.exists() else []

    def assert_stopped_on_restart(self, first):
        self.assertNotEqual(first.returncode, 0, first.stdout + first.stderr)
        self.assertTrue(self.genesis.exists())
        events = self.events()
        self.assertNotIn("start", events)
        second = self.run_genesis()
        self.assertNotEqual(second.returncode, 0, second.stdout + second.stderr)
        self.assertTrue("incomplete mocknet genesis" in second.stderr.lower())
        self.assertEqual(self.events(), events)

    def test_partial_deployment_cannot_start_or_redeploy_on_restart(self):
        self.assert_stopped_on_restart(self.run_genesis(TEST_DEPLOY="failure"))
        self.assertEqual((self.home / "eth-contract").read_text(), "deployed\n")
        self.assertEqual(self.events().count("deploy"), 1)

    def test_interrupted_deployment_cannot_start_on_restart(self):
        self.assert_stopped_on_restart(self.run_genesis(TEST_DEPLOY="interrupted"))

    def test_failed_validation_cannot_start_on_restart(self):
        self.assert_stopped_on_restart(self.run_genesis(TEST_VALIDATE_EXIT="1"))

    def test_interruption_before_genesis_creation_still_blocks_restart(self):
        first = self.run_genesis(TEST_INIT="interrupted")
        self.assertNotEqual(first.returncode, 0)
        self.assertFalse(self.genesis.exists())
        second = self.run_genesis()
        self.assertNotEqual(second.returncode, 0, second.stdout + second.stderr)
        self.assertEqual(self.events(), ["init"])

    def test_successful_boot_restarts_without_reinitialization(self):
        for _ in range(2):
            result = self.run_genesis()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(
            self.events(),
            [
                "init",
                "deploy",
                "validate",
                "render-config",
                "start",
                "render-config",
                "start",
            ],
        )

    def test_evm_disabled_boot_still_validates_and_restarts(self):
        for _ in range(2):
            result = self.run_genesis(SKIP_EVM_DEPLOY="true")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.events().count("validate"), 1)
        self.assertNotIn("deploy", self.events())
        self.assertEqual(self.events().count("start"), 2)

    def test_existing_genesis_without_marker_remains_supported(self):
        self.genesis.parent.mkdir(parents=True)
        self.genesis.write_text("{}")
        result = self.run_genesis()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.events(), ["render-config", "start"])

    def test_non_mocknet_boot_and_restart_do_not_require_marker(self):
        for _ in range(2):
            result = self.run_genesis(NET="mainnet")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.events().count("init"), 1)
        self.assertEqual(self.events().count("start"), 2)
        self.assertNotIn("deploy", self.events())
        self.assertFalse(
            (self.home / ".thornode/.mocknet-genesis-in-progress").exists()
        )


if __name__ == "__main__":
    unittest.main()
