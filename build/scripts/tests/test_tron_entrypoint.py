"""Exercise the real TRON entrypoint with the final JVM launch intercepted."""

import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "docker/mocknet/tron-entrypoint.sh"
CONFIG = """node {
  rpc {
SETTING  }
}
block {
  needSyncCheck = true
}
"""
LAUNCH_STUB = """exec() {
  [ "$1" = /usr/bin/java ] || exit 99
  shift
  printf '%s\\n' "$@" > "$HOME/java-args"
  exit 0
}
"""


class TronEntrypointTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="tron-entrypoint-")
        self.addCleanup(self.tmp.cleanup)
        self.home = Path(self.tmp.name) / "node home"
        self.home.mkdir()
        self.config = self.home / "mocknet-fullnode.conf"
        self.launch_args = self.home / "java-args"
        self.bash_env = self.home / "launch-stub.sh"
        self.bash_env.write_text(LAUNCH_STUB)
        self.env = {
            "HOME": str(self.home),
            "PATH": os.defpath,
            "BASH_ENV": str(self.bash_env),
            "TRON_NODE_TYPE": "mocknet-fullnode",
        }

    def run_entrypoint(self, node_type="mocknet-fullnode"):
        return subprocess.run(
            ["bash", str(SCRIPT)],
            env={**self.env, "TRON_NODE_TYPE": node_type},
            capture_output=True,
            text=True,
            timeout=10,
        )

    def test_default_setting_requires_a_producer_and_preserves_sync_setting(self):
        self.config.write_text(
            CONFIG.replace("SETTING", "    minEffectiveConnection = 0\n")
        )
        result = self.run_entrypoint()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(
            self.config.read_text(),
            CONFIG.replace("SETTING", "    minEffectiveConnection = 1\n"),
        )
        self.assertEqual(
            self.launch_args.read_text().splitlines(),
            [
                "-Xmx1536m",
                "-XX:+HeapDumpOnOutOfMemoryError",
                "-Dlogback.configurationFile=/scripts/logback.xml",
                "-jar",
                "FullNode.jar",
                "-c",
                "mocknet-fullnode.conf",
            ],
        )

    def test_already_configured_setting_is_unchanged_across_restarts(self):
        config = CONFIG.replace("SETTING", "    minEffectiveConnection = 1\n")
        self.config.write_text(config)
        for _ in range(2):
            self.launch_args.unlink(missing_ok=True)
            result = self.run_entrypoint()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(self.config.read_text(), config)
            self.assertTrue(self.launch_args.exists())
            self.assertFalse(self.config.with_suffix(".conf.tmp").exists())

    def test_spacing_and_comments_are_preserved(self):
        for value in (0, 1):
            for comment in ("# old example = 0", "// old example = 0"):
                with self.subTest(value=value, comment=comment):
                    setting = f"\tminEffectiveConnection\t=\t{value}  {comment}\n"
                    config = CONFIG.replace("SETTING", setting)
                    self.config.write_text(config)
                    result = self.run_entrypoint()
                    self.assertEqual(
                        result.returncode, 0, result.stdout + result.stderr
                    )
                    self.assertEqual(
                        self.config.read_text(), config.replace("=\t0", "= 1")
                    )

    def test_invalid_settings_stop_before_java_without_replacing_config(self):
        settings = {
            "missing": "",
            "commented": "    # minEffectiveConnection = 0\n",
            "commented_slashes": "    // minEffectiveConnection = 0\n",
            "unsupported_value": "    minEffectiveConnection = 2\n",
            "multiple_digits": "    minEffectiveConnection = 10\n",
            "boolean": "    minEffectiveConnection = true\n",
            "empty": "    minEffectiveConnection =\n",
            "trailing_text": "    minEffectiveConnection = 0junk\n",
            "unsupported_separator": "    minEffectiveConnection: 0\n",
            "duplicate": "    minEffectiveConnection = 0\n    minEffectiveConnection = 1\n",
            "duplicate_correct": "    minEffectiveConnection = 1\n    minEffectiveConnection = 1\n",
            "quoted_override": '    minEffectiveConnection = 1\n    "minEffectiveConnection" = 0\n',
            "dotted_override": "    minEffectiveConnection = 1\n    node.rpc.minEffectiveConnection = 0\n",
        }
        for case, setting in settings.items():
            with self.subTest(case=case):
                config = CONFIG.replace("SETTING", setting)
                self.config.write_text(config)
                result = self.run_entrypoint()
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue("Invalid TRON mocknet config" in result.stderr)
                self.assertEqual(self.config.read_text(), config)
                self.assertFalse(self.launch_args.exists())
                self.assertFalse(self.config.with_suffix(".conf.tmp").exists())

    def test_other_profiles_do_not_require_the_fullnode_setting(self):
        for node_type in ("mainnet", "testnet-nile", "mocknet-supernode"):
            with self.subTest(node_type=node_type):
                config = self.home / f"{node_type}.conf"
                config.write_text("minParticipationRate = 15\n")
                self.launch_args.unlink(missing_ok=True)
                result = self.run_entrypoint(node_type)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(self.launch_args.exists())
                expected = "0" if node_type == "mocknet-supernode" else "15"
                self.assertEqual(
                    config.read_text(), f"minParticipationRate = {expected}\n"
                )


if __name__ == "__main__":
    unittest.main()
