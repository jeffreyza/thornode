"""Check the effective XMR deployment settings after Compose inheritance."""

import copy
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[3]
TEST_TOKEN = "xmr-readiness-contract-test-token"
SIGNER_REPOSITORY = "registry.gitlab.com/thorchain/devops/serai/xmr-frost-signer"
SUFFIXES = ("", "-cat", "-fox", "-pig", "-frog")
DOCKER_SETTINGS = (
    "DOCKER_HOST",
    "DOCKER_TLS_CERTDIR",
    "DOCKER_TLS_VERIFY",
    "DOCKER_CERT_PATH",
)
PROFILES = {
    "mocknet": ("docker-compose.yml", "mocknet", 5),
    "chainnet": ("docker-compose.xmr-chainnet.yml", "chainnet", 3),
    "stagenet": ("docker-compose.xmr-stagenet.yml", "mocknet", 3),
}


def render(profile, token=TEST_TOKEN, *, xmr_disabled="false", jail_time_keysign=None):
    env = os.environ.copy()
    # Test the default independently of the caller's simulation jail override.
    env.pop("JAIL_TIME_KEYSIGN", None)
    if jail_time_keysign is not None:
        env["JAIL_TIME_KEYSIGN"] = str(jail_time_keysign)
    # Validate XMR readiness even when the caller disables XMR for a cluster run.
    # Only this rendering subprocess receives the controlled setting.
    env["BIFROST_CHAINS_XMR_DISABLED"] = xmr_disabled
    # Config rendering needs neither a daemon nor inherited CI TLS credentials.
    for key in (
        *DOCKER_SETTINGS,
        "XMR_FROST_SIGNER_IMAGE",
        "XMR_SIM_HELPER_IMAGE",
        "XMR_FROST_AUTH_BEARER",
        "XMR_SIM_HELPER_AUTH_BEARER",
    ):
        env.pop(key, None)
    if token is not None:
        env["XMR_FROST_AUTH_BEARER"] = token
        env["XMR_SIM_HELPER_AUTH_BEARER"] = token
    filename = PROFILES[profile][0]
    return subprocess.run(
        [
            "docker",
            "compose",
            "--env-file",
            "/dev/null",
            "-p",
            "xmr-readiness-contract",
            "-f",
            str(ROOT / "build/docker" / filename),
            "--profile",
            "*",
            "config",
            "--format",
            "json",
        ],
        env=env,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


def seed_identity(service, model):
    env = service.get("environment", {})
    if env.get("SIGNER_SEED_PHRASE"):
        return env["SIGNER_SEED_PHRASE"].strip()
    seed_path = env.get("SIGNER_SEED_PHRASE_FILE")
    for mounted in service.get("configs", []):
        if mounted.get("target") == seed_path:
            seed_file = Path(model["configs"][mounted["source"]]["file"]).resolve()
            if not seed_file.is_relative_to(ROOT):
                raise AssertionError(
                    "readiness tests require a repository-owned test seed file"
                )
            return seed_file.read_text().strip()
    return None


class XMRReadinessConfigTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.models = {}
        for profile in PROFILES:
            result = render(profile)
            if result.returncode:
                raise AssertionError(
                    f"{profile} Compose configuration failed: {result.stderr}"
                )
            cls.models[profile] = json.loads(result.stdout)

    def assert_publication(self, service, target, published=None):
        ports = service.get("ports", [])
        if published is None:
            self.assertEqual(ports, [], "secondary private signer must not publish")
            return
        self.assertEqual(len(ports), 1, "only the loopback API port may be published")
        self.assertEqual(ports[0].get("host_ip"), "127.0.0.1")
        self.assertEqual(ports[0].get("target"), target)
        self.assertEqual(ports[0].get("published"), str(published))
        self.assertEqual(ports[0].get("protocol"), "tcp")

    def assert_healthcheck(self, service, port):
        healthcheck = service.get("healthcheck", {})
        self.assertFalse(healthcheck.get("disable", False), "readiness is disabled")
        test = healthcheck.get("test", [])
        self.assertEqual(len(test), 2, "a shell readiness command is required")
        self.assertEqual(test[0], "CMD-SHELL")
        # Compose versions may retain one or both dollar signs. In either case
        # the bearer must be expanded inside the container, not by Compose.
        command = test[1].replace("$$", "$")
        self.assertNotIn(TEST_TOKEN, command, "bearer expanded during rendering")
        args = shlex.split(command)
        self.assertTrue(
            "Authorization: Bearer ${XMR_FROST_AUTH_BEARER}" in args,
            "readiness must use the container-time bearer",
        )
        self.assertTrue(
            f"http://127.0.0.1:{port}/v1/monero/chain/height" in args,
            f"readiness must use the loopback endpoint on {port}",
        )
        self.assertTrue("curl" in args, "curl readiness command is required")
        first_argument = args.index("curl") + 1
        curl_args = args[first_argument:]
        self.assertTrue(
            any(
                arg in ("--fail", "--fail-with-body")
                or (arg.startswith("-") and not arg.startswith("--") and "f" in arg)
                for arg in curl_args
            ),
            "readiness must reject HTTP failures",
        )

    def assert_data_volume(self, model, name):
        service = model["services"][name]
        helper = name == "xmr-sim-helper"
        directory = "/home/serai/xmr-sim" if helper else "/home/serai/xmr-frost"
        volume = "xmr-sim-data" if helper else name.replace("signer", "data")
        self.assertEqual(service["environment"].get("XMR_FROST_DATA_DIR"), directory)
        mounts = [v for v in service.get("volumes", []) if v["target"] == directory]
        self.assertEqual(len(mounts), 1, "exactly one data mount is required")
        self.assertEqual(mounts[0].get("type"), "volume")
        self.assertEqual(mounts[0].get("source"), volume, "wrong per-node data volume")
        self.assertFalse(mounts[0].get("read_only", False), "data must be writable")
        engine_name = model.get("volumes", {}).get(volume, {}).get("name")
        self.assertTrue(engine_name, "data volume must have an engine name")
        return engine_name

    def assert_isolated_data_volumes(self, model):
        names = [
            self.assert_data_volume(model, name)
            for name in model["services"]
            if name.startswith("xmr-frost-signer") or name == "xmr-sim-helper"
        ]
        # Logical Compose keys can still alias one engine volume via `name`.
        self.assertEqual(len(names), len(set(names)), "data volumes must be isolated")

    def assert_healthy_dependency(self, service, dependency):
        self.assertEqual(
            service.get("depends_on", {}).get(dependency, {}).get("condition"),
            "service_healthy",
            f"must wait for healthy {dependency}",
        )

    def assert_restart_policy(self, service):
        self.assertEqual(service.get("restart"), "unless-stopped")
        self.assertEqual(service.get("stop_signal", "SIGTERM"), "SIGTERM")

    def assert_signer_environment(self, service, network):
        env = service["environment"]
        for key, value in {
            "XMR_FROST_CHAIN_NETWORK": network,
            "XMR_FROST_DAEMON_URL": "http://monero-daemon:18081",
            "XMR_FROST_LISTEN_ADDR": "0.0.0.0:8283",
            "XMR_FROST_AUTH_BEARER": TEST_TOKEN,
            "XMR_FROST_MAX_REQUESTS_PER_SECOND": "256",
            "XMR_FROST_MAX_REQUESTS_PER_IP_SECOND": "64",
        }.items():
            self.assertEqual(env.get(key), value, key)
        for key in (
            "XMR_FROST_AUTH_USERNAME",
            "XMR_FROST_AUTH_PASSWORD",
            "XMR_FROST_DAEMON_DISABLE_TLS",
            "XMR_FROST_ADMIN_ALLOW_NON_LOOPBACK",
            "XMR_SIM_HELPER_ENABLED",
            "XMR_SIM_HELPER_WALLET_RPC_URL",
        ):
            self.assertNotIn(key, env)
        self.assertFalse(
            any(k.startswith("XMR_FROST_RATE_LIMIT_") for k in env),
            "legacy rate-limit settings do not configure this signer image",
        )

    def assert_helper_image(self, model):
        services = model["services"]
        image = services["xmr-sim-helper"]["image"]
        self.assertRegex(
            image, rf"^{re.escape(SIGNER_REPOSITORY)}@sha256:[0-9a-f]{{64}}$"
        )
        self.assertNotEqual(
            image.rsplit("@", 1)[1],
            services["xmr-frost-signer"]["image"].rsplit("@", 1)[1],
            "helper must use a different faucet-capable digest",
        )

    def assert_mocknet_bearer_defaults(self, model):
        for suffix in SUFFIXES:
            services = model["services"]
            self.assertEqual(
                services[f"bifrost{suffix}"]["environment"].get(
                    "BIFROST_CHAINS_XMR_SIGNER_AUTHORIZATION_BEARER"
                ),
                "xmr-frost-mocknet-token",
            )
            self.assertEqual(
                services[f"xmr-frost-signer{suffix}"]["environment"].get(
                    "XMR_FROST_AUTH_BEARER"
                ),
                "xmr-frost-mocknet-token",
            )
        self.assertEqual(
            model["services"]["xmr-sim-helper"]["environment"].get(
                "XMR_FROST_AUTH_BEARER"
            ),
            "xmr-sim-helper-mocknet-token",
        )

    def test_render_controls_xmr_without_changing_the_caller_environment(self):
        setting = "BIFROST_CHAINS_XMR_DISABLED"
        with patch.dict(os.environ, {setting: "true"}):
            result = render("mocknet")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(os.environ[setting], "true")
            bifrosts = {
                name: service
                for name, service in json.loads(result.stdout)["services"].items()
                if name.startswith("bifrost")
            }
            self.assertEqual(len(bifrosts), PROFILES["mocknet"][2])
            for name, service in bifrosts.items():
                with self.subTest(service=name):
                    self.assertEqual(service["environment"][setting], "false")

    def test_render_ignores_caller_images_credentials_and_daemon_settings(self):
        overrides = {
            **dict.fromkeys(DOCKER_SETTINGS, "invalid-contract-test-setting"),
            "XMR_FROST_SIGNER_IMAGE": "caller-signer:latest",
            "XMR_SIM_HELPER_IMAGE": "caller-helper:latest",
            "XMR_FROST_AUTH_BEARER": "caller-signer-token",
            "XMR_SIM_HELPER_AUTH_BEARER": "caller-helper-token",
        }
        with patch.dict(os.environ, overrides):
            caller_env = os.environ.copy()
            result = render("mocknet")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout), self.models["mocknet"])
            self.assertEqual(os.environ, caller_env)

    def test_keygen_recovery_override_only_reaches_pig(self):
        setting = "XMR_KEYGEN_RECOVERY_ROUND3_TIMEOUT_MS"
        for value in ("", "100"):
            with self.subTest(timeout=value), patch.dict(os.environ, {setting: value}):
                result = render("mocknet")
                self.assertEqual(result.returncode, 0, result.stderr)
                services = json.loads(result.stdout)["services"]
                for name, service in services.items():
                    if not name.startswith("bifrost"):
                        continue
                    actual = service.get("environment", {}).get(
                        "XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS"
                    )
                    if name == "bifrost-pig" and value:
                        self.assertEqual(str(actual), value)
                    else:
                        self.assertTrue(actual in (None, ""))

    def test_mocknet_still_honors_explicit_xmr_disable(self):
        result = render("mocknet", xmr_disabled="true")
        self.assertEqual(result.returncode, 0, result.stderr)
        bifrosts = {
            name: service
            for name, service in json.loads(result.stdout)["services"].items()
            if name.startswith("bifrost")
        }
        self.assertEqual(len(bifrosts), PROFILES["mocknet"][2])
        for name, service in bifrosts.items():
            with self.subTest(service=name):
                self.assertEqual(
                    service["environment"]["BIFROST_CHAINS_XMR_DISABLED"], "true"
                )

    def test_mocknet_keysign_timeout_and_jail_are_consistent_across_nodes(self):
        services = self.models["mocknet"]["services"]
        for suffix in SUFFIXES:
            with self.subTest(node=suffix or "primary"):
                thornode_env = services[f"thornode{suffix}"]["environment"]
                bifrost_env = services[f"bifrost{suffix}"]["environment"]
                self.assertEqual(
                    bifrost_env.get("BIFROST_SIGNER_KEYSIGN_TIMEOUT"), "45s"
                )
                # At mocknet's one-second block time, jail must outlast signing.
                # Bifrost checks its local constant; THORNode enforces jail.
                self.assertEqual(thornode_env.get("JAIL_TIME_KEYSIGN"), "60")
                self.assertEqual(bifrost_env.get("JAIL_TIME_KEYSIGN"), "60")

    def test_mocknet_keysign_jail_defaults_and_override_are_isolated(self):
        setting = "JAIL_TIME_KEYSIGN"
        for inherited in (None, "600"):
            with patch.dict(os.environ):
                os.environ.pop(setting, None)
                if inherited is not None:
                    os.environ[setting] = inherited
                caller_env = os.environ.copy()
                for override, expected in ((None, "60"), (600, "600")):
                    with self.subTest(inherited=inherited, override=override):
                        result = render("mocknet", jail_time_keysign=override)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertTrue(
                            os.environ == caller_env,
                            "render must not change the caller's environment",
                        )
                        services = json.loads(result.stdout)["services"]
                        for role in ("thornode", "bifrost"):
                            nodes = {
                                name: service
                                for name, service in services.items()
                                if name.startswith(role)
                            }
                            self.assertEqual(len(nodes), PROFILES["mocknet"][2])
                            for name, service in nodes.items():
                                with self.subTest(service=name):
                                    self.assertEqual(
                                        service["environment"].get(setting), expected
                                    )

    def test_mocknet_xmr_scanner_uses_simulation_backoff(self):
        for name, service in self.models["mocknet"]["services"].items():
            if not name.startswith("bifrost"):
                continue
            with self.subTest(service=name):
                env = service["environment"]
                self.assertEqual(
                    env.get(
                        "BIFROST_CHAINS_XMR_BLOCK_SCANNER_BLOCK_HEIGHT_DISCOVER_BACK_OFF"
                    ),
                    env["BLOCK_SCANNER_BACKOFF"],
                )

    def test_each_bifrost_can_authenticate_to_its_matching_signer(self):
        for profile, model in self.models.items():
            services = model["services"]
            _, network, count = PROFILES[profile]
            bifrosts = {n: s for n, s in services.items() if n.startswith("bifrost")}
            self.assertEqual(len(bifrosts), count, profile)
            signers = set()
            for name, bifrost in bifrosts.items():
                with self.subTest(profile=profile, service=name):
                    env = bifrost["environment"]
                    self.assertEqual(env.get("BIFROST_CHAINS_XMR_DISABLED"), "false")
                    endpoint = urlsplit(env["BIFROST_CHAINS_XMR_SIGNER_ENDPOINT"])
                    self.assertEqual(endpoint.scheme, "http")
                    suffix = name.removeprefix("bifrost")
                    self.assertEqual(endpoint.hostname, f"xmr-frost-signer{suffix}")
                    self.assertEqual(endpoint.port, 8283)
                    self.assertEqual(endpoint.path, "")
                    signers.add(endpoint.hostname)
                    signer_env = services[endpoint.hostname]["environment"]
                    self.assertEqual(
                        env.get("BIFROST_CHAINS_XMR_SIGNER_EXPECTED_NETWORK"), network
                    )
                    self.assertEqual(signer_env.get("XMR_FROST_CHAIN_NETWORK"), network)
                    self.assertEqual(
                        env.get("BIFROST_CHAINS_XMR_SIGNER_ALLOW_INSECURE_HTTP"), "true"
                    )
                    bearer = env.get("BIFROST_CHAINS_XMR_SIGNER_AUTHORIZATION_BEARER")
                    self.assertEqual(bearer, TEST_TOKEN)
                    self.assertTrue(
                        bearer == signer_env.get("XMR_FROST_AUTH_BEARER"),
                        "Bifrost and signer authentication must match",
                    )
                    for key in (
                        "BIFROST_CHAINS_XMR_AUTHORIZATION_BEARER",
                        "BIFROST_CHAINS_XMR_SIGNER_USERNAME",
                        "BIFROST_CHAINS_XMR_SIGNER_PASSWORD",
                    ):
                        self.assertNotIn(key, env)
                    self.assertEqual(
                        env.get("BIFROST_CHAINS_XMR_RPC_HOST"),
                        "http://monero-daemon:18081",
                    )
                    self.assertEqual(
                        env.get("BIFROST_CHAINS_XMR_CHAIN_NETWORK"),
                        "mocknet" if profile == "mocknet" else "mainnet",
                    )
            self.assertEqual(len(signers), count, "each validator needs its own signer")

    def test_validator_and_signer_seed_assignments_match(self):
        for profile, model in self.models.items():
            identities = set()
            for name, service in model["services"].items():
                if not name.startswith("bifrost"):
                    continue
                with self.subTest(profile=profile, service=name):
                    endpoint = urlsplit(
                        service["environment"]["BIFROST_CHAINS_XMR_SIGNER_ENDPOINT"]
                    )
                    identity = seed_identity(service, model)
                    self.assertTrue(
                        identity, "Bifrost seed must be explicitly assigned"
                    )
                    self.assertTrue(
                        identity
                        == seed_identity(model["services"][endpoint.hostname], model),
                        "validator and signer seed assignments must match",
                    )
                    suffix = name.removeprefix("bifrost")
                    self.assertEqual(
                        identity,
                        seed_identity(model["services"][f"thornode{suffix}"], model),
                    )
                    for role in ("thornode", "bifrost", "xmr-frost-signer"):
                        node = model["services"][f"{role}{suffix}"]
                        env = node["environment"]
                        if profile == "chainnet":
                            label = suffix.removeprefix("-") or "dog"
                            source = f"seed-{label}"
                            self.assertNotIn("SIGNER_SEED_PHRASE", env)
                            self.assertEqual(
                                env.get("SIGNER_SEED_PHRASE_FILE"),
                                "/run/thornode/seed_phrase",
                            )
                            self.assertEqual(
                                node.get("configs"),
                                [
                                    {
                                        "source": source,
                                        "target": "/run/thornode/seed_phrase",
                                    }
                                ],
                            )
                            self.assertEqual(
                                Path(model["configs"][source]["file"]),
                                ROOT / f"build/docker/chainnet-seeds/{label}.txt",
                            )
                        else:
                            self.assertNotIn("SIGNER_SEED_PHRASE_FILE", env)
                            self.assertTrue(env.get("SIGNER_SEED_PHRASE"))
                    self.assertNotIn(
                        identity,
                        identities,
                        "validators must have distinct seed assignments",
                    )
                    identities.add(identity)
            if profile == "chainnet":
                self.assertEqual(
                    set(model["configs"]), {"seed-dog", "seed-cat", "seed-fox"}
                )

    def test_signers_share_an_immutable_image_and_supported_rate_limits(self):
        images = set()
        for profile, model in self.models.items():
            for name, service in model["services"].items():
                if not name.startswith("xmr-frost-signer"):
                    continue
                with self.subTest(profile=profile, service=name):
                    self.assertRegex(
                        service["image"],
                        rf"^{re.escape(SIGNER_REPOSITORY)}:[^@\s]+@sha256:[0-9a-f]{{64}}$",
                    )
                    self.assertNotIn(":latest@", service["image"])
                    images.add(service["image"])
                    self.assert_signer_environment(service, PROFILES[profile][1])
        self.assertEqual(
            len(images), 1, "all profiles must use the same reviewed signer image"
        )

    def test_private_profiles_require_explicit_authentication(self):
        for profile in ("chainnet", "stagenet"):
            source = (ROOT / "build/docker" / PROFILES[profile][0]).read_text()
            self.assertNotIn("${XMR_FROST_AUTH_BEARER:-", source)
            for token in (None, ""):
                with self.subTest(profile=profile, token=token):
                    result = render(profile, token=token)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertTrue("XMR_FROST_AUTH_BEARER" in result.stderr)
                    self.assertTrue("strong random token" in result.stderr)

    def test_mocknet_credentials_have_independent_local_defaults(self):
        result = render("mocknet", token=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_mocknet_bearer_defaults(json.loads(result.stdout))

    def test_api_publication_is_loopback_only(self):
        for profile, model in self.models.items():
            for index, suffix in enumerate(SUFFIXES[: PROFILES[profile][2]]):
                with self.subTest(profile=profile, node=suffix or "primary"):
                    port = 8283 if not suffix else 8284 + index
                    if profile != "mocknet" and suffix:
                        port = None
                    self.assert_publication(
                        model["services"][f"xmr-frost-signer{suffix}"], 8283, port
                    )
        self.assert_publication(
            self.models["mocknet"]["services"]["xmr-sim-helper"], 8284, 8284
        )

    def test_readiness_uses_the_container_bearer_and_rejects_http_errors(self):
        for profile, model in self.models.items():
            for name, service in model["services"].items():
                if name.startswith("xmr-frost-signer") or name == "xmr-sim-helper":
                    with self.subTest(profile=profile, service=name):
                        self.assert_healthcheck(
                            service, 8284 if name == "xmr-sim-helper" else 8283
                        )

    def test_signer_and_helper_data_volumes_are_writable_and_isolated(self):
        for profile, model in self.models.items():
            with self.subTest(profile=profile):
                self.assert_isolated_data_volumes(model)

    def test_services_use_private_networks_and_healthy_dependencies(self):
        for profile, model in self.models.items():
            self.assertFalse(model["networks"]["default"].get("external", False))
            for name, service in model["services"].items():
                if (
                    name != "monero-daemon"
                    and name != "monero-wallet-rpc"
                    and name != "xmr-sim-helper"
                    and not name.startswith(("thornode", "bifrost", "xmr-frost-signer"))
                ):
                    continue
                with self.subTest(profile=profile, service=name):
                    self.assertEqual(set(service.get("networks", {})), {"default"})
                    self.assertNotIn("network_mode", service)
                    if name.startswith("xmr-frost-signer") or name == "xmr-sim-helper":
                        self.assert_restart_policy(service)
                        self.assert_healthy_dependency(service, "monero-daemon")
                    if name.startswith("bifrost") and profile != "mocknet":
                        suffix = name.removeprefix("bifrost")
                        self.assertEqual(
                            service["environment"].get("NET"), PROFILES[profile][1]
                        )
                        self.assertEqual(
                            model["services"][f"thornode{suffix}"]["environment"].get(
                                "NET"
                            ),
                            PROFILES[profile][1],
                        )
                        for dependency in (
                            "monero-daemon",
                            f"thornode{suffix}",
                            f"xmr-frost-signer{suffix}",
                        ):
                            self.assert_healthy_dependency(service, dependency)
                    if name == "xmr-sim-helper":
                        for dependency in ("monero-wallet-rpc", "xmr-frost-signer"):
                            self.assert_healthy_dependency(service, dependency)
                    if name == "monero-wallet-rpc":
                        self.assert_healthy_dependency(service, "monero-daemon")

    def test_helper_retains_its_faucet_image_and_environment(self):
        model = self.models["mocknet"]
        self.assert_helper_image(model)
        env = model["services"]["xmr-sim-helper"]["environment"]
        for key, value in {
            "XMR_FROST_CHAIN_NETWORK": "mocknet",
            "XMR_FROST_AUTH_BEARER": TEST_TOKEN,
            "XMR_SIM_HELPER_ENABLED": "true",
            "XMR_SIM_HELPER_WALLET_RPC_URL": "http://monero-wallet-rpc:18082",
            "XMR_FROST_DAEMON_URL": "http://monero-daemon:18081",
            "XMR_FROST_LISTEN_ADDR": "0.0.0.0:8284",
        }.items():
            self.assertEqual(env.get(key), value, key)

    def test_private_profiles_run_mainnet_monero(self):
        for profile in ("chainnet", "stagenet"):
            with self.subTest(profile=profile):
                command = self.models[profile]["services"]["monero-daemon"]["command"]
                self.assertFalse(
                    any(
                        arg.startswith(("--stagenet", "--testnet", "--regtest"))
                        for arg in command
                    )
                )

    def test_invalid_rendered_contracts_are_rejected(self):
        model = self.models["mocknet"]
        signer = "xmr-frost-signer"
        helper = "xmr-sim-helper"

        def reject(path, value, check, message):
            with self.subTest(path=path):
                invalid = copy.deepcopy(model)
                parent = invalid
                for key in path[:-1]:
                    parent = parent[key]
                parent[path[-1]] = value
                with self.assertRaisesRegex(AssertionError, message):
                    check(invalid)

        # Different tag syntax must not disguise reuse of the non-faucet build.
        digest = model["services"][signer]["image"].rsplit("@", 1)[1]
        reject(
            ("services", helper, "image"),
            f"{SIGNER_REPOSITORY}@{digest}",
            self.assert_helper_image,
            "different faucet-capable digest",
        )
        reject(
            ("services", "xmr-frost-signer-cat", "volumes", 0, "source"),
            "xmr-frost-data",
            self.assert_isolated_data_volumes,
            "wrong per-node data volume",
        )
        for volume in ("xmr-frost-data-cat", "xmr-sim-data"):
            reject(
                ("volumes", volume, "name"),
                model["volumes"]["xmr-frost-data"]["name"],
                self.assert_isolated_data_volumes,
                "isolated",
            )
        reject(
            ("services", helper, "volumes", 0, "read_only"),
            True,
            self.assert_isolated_data_volumes,
            "writable",
        )
        reject(
            ("services", signer, "healthcheck", "disable"),
            True,
            lambda m: self.assert_healthcheck(m["services"][signer], 8283),
            "disabled",
        )
        healthcheck = model["services"][helper]["healthcheck"]["test"]
        for old, new, message in (
            (":8284/", ":8283/", "8284"),
            ("-fsS", "-sS", "HTTP failures"),
        ):
            reject(
                ("services", helper, "healthcheck", "test"),
                [healthcheck[0], healthcheck[1].replace(old, new)],
                lambda m: self.assert_healthcheck(m["services"][helper], 8284),
                message,
            )
        reject(
            ("services", signer, "stop_signal"),
            "SIGKILL",
            lambda m: self.assert_restart_policy(m["services"][signer]),
            "SIGTERM",
        )
        reject(
            ("services", helper, "depends_on", "monero-wallet-rpc", "condition"),
            "service_started",
            lambda m: self.assert_healthy_dependency(
                m["services"][helper], "monero-wallet-rpc"
            ),
            "healthy monero-wallet-rpc",
        )
        reject(
            ("services", signer, "environment", "XMR_SIM_HELPER_ENABLED"),
            "true",
            lambda m: self.assert_signer_environment(m["services"][signer], "mocknet"),
            "XMR_SIM_HELPER_ENABLED",
        )
        public_port = {
            "host_ip": "0.0.0.0",
            "target": 9999,
            "published": "9999",
            "protocol": "tcp",
        }
        reject(
            ("services", signer, "ports"),
            model["services"][signer]["ports"] + [public_port],
            lambda m: self.assert_publication(m["services"][signer], 8283, 8283),
            "loopback API",
        )
        result = render("mocknet", token=None)
        self.assertEqual(result.returncode, 0, result.stderr)
        invalid = json.loads(result.stdout)
        invalid["services"]["bifrost-fox"]["environment"][
            "BIFROST_CHAINS_XMR_SIGNER_AUTHORIZATION_BEARER"
        ] = "xmr-sim-helper-mocknet-token"
        with self.assertRaisesRegex(AssertionError, "xmr-frost-mocknet-token"):
            self.assert_mocknet_bearer_defaults(invalid)


class XMRResetScannerAuthTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="xmr-reset-contract-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.log = self.root / "calls.jsonl"
        self.mktemp = shutil.which("mktemp")
        self.assertTrue(self.mktemp, "reset tests require mktemp")
        # Execute the container-side request using fixture credentials and local
        # stubs; redirect its payload into the temporary directory. Neither a
        # Docker daemon nor an HTTP server is used by these reset tests.
        program = (
            f"#!{sys.executable}\n"
            + """
import json
import os
import subprocess
import sys
from pathlib import Path

args = sys.argv[1:]
name = Path(sys.argv[0]).name
root = Path(os.environ["TEST_RESET_ROOT"])
call = {"command": name, "args": args,
        "bearer": os.environ.get("XMR_FROST_AUTH_BEARER")}
if name == "docker" and args[0] == "compose":
    call["override"] = Path(args[args.index("-f", 2) + 1]).read_text()
if name == "curl":
    call["payload"] = json.loads(Path(args[args.index("--data") + 1][1:]).read_text())
with (root / "calls.jsonl").open("a") as log:
    log.write(json.dumps(call) + "\\n")
if name == "mktemp":
    # Enforce the common GNU/BusyBox template contract even on GNU-only hosts.
    if not args or not args[-1].endswith("XXXXXX"):
        sys.exit("mktemp template must end with XXXXXX for BusyBox compatibility")
    sys.exit(subprocess.run([os.environ["TEST_MKTEMP"], *args], check=False).returncode)
if name in ("jq", "curl"):
    sys.exit(0)
if args[:2] == ["exec", "signer-test"] or args[:3] == ["exec", "-i", "signer-test"]:
    command = args[-1].replace("/tmp/xmr-reset-scanner-payload.json", str(root / "payload.json"))
    env = os.environ.copy()
    env["XMR_FROST_AUTH_BEARER"] = env["TEST_SIGNER_BEARER"]
    env.pop("SIGNER_SEED_PHRASE", None)
    sys.exit(subprocess.run(["sh", "-c", command], env=env, check=False).returncode)
elif args[0] != "compose":
    sys.exit("unexpected mocked Docker command")
"""
        )
        for command in ("docker", "curl", "jq", "mktemp"):
            executable = self.root / command
            executable.write_text(program)
            executable.chmod(0o755)

    def run_reset(self, signer_bearer=TEST_TOKEN, host_bearer=None):
        env = os.environ.copy()
        env.pop("XMR_FROST_AUTH_BEARER", None)
        env.update(
            PATH=f"{self.root}:{env['PATH']}",
            TEST_RESET_ROOT=str(self.root),
            TEST_SIGNER_BEARER=signer_bearer,
            TEST_MKTEMP=self.mktemp,
            TMPDIR=str(self.root),
        )
        if host_bearer is not None:
            env["XMR_FROST_AUTH_BEARER"] = host_bearer
        return subprocess.run(
            [
                "bash",
                str(ROOT / "build/docker/xmr-reset-scanner.sh"),
                "--vault-pubkey",
                "contract-test-vault",
                "--height",
                "42",
                "--signer-container",
                "signer-test",
                "--bifrost-container",
                "bifrost-test",
                "--bifrost-service",
                "bifrost",
                "--compose-file",
                str(ROOT / "build/docker/docker-compose.xmr-chainnet.yml"),
            ],
            env=env,
            capture_output=True,
            text=True,
            check=False,
            timeout=30,
        )

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_reset_uses_and_exports_the_configured_bearer_without_printing_it(self):
        for host_bearer in (None, TEST_TOKEN):
            with self.subTest(host_bearer=host_bearer):
                self.log.unlink(missing_ok=True)
                result = self.run_reset(host_bearer=host_bearer)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotIn(TEST_TOKEN, result.stdout + result.stderr)
                calls = self.calls()
                requests = [call for call in calls if call["command"] == "curl"]
                self.assertEqual(len(requests), 1)
                self.assertTrue(
                    f"Authorization: Bearer {TEST_TOKEN}" in requests[0]["args"]
                )
                self.assertTrue(
                    "http://localhost:8283/v1/monero/scanner/reset"
                    in requests[0]["args"]
                )
                self.assertEqual(
                    requests[0]["payload"],
                    {"vault_pub_key": "contract-test-vault", "birthday_height": 42},
                )
                recreations = [call for call in calls if call["args"][0] == "compose"]
                self.assertEqual(len(recreations), 1)
                self.assertEqual(recreations[0]["bearer"], TEST_TOKEN)
                self.assertTrue(
                    'BIFROST_CHAINS_XMR_BLOCK_SCANNER_START_BLOCK_HEIGHT: "42"'
                    in recreations[0]["override"]
                )

    def test_reset_rejects_missing_or_conflicting_credentials_before_side_effects(self):
        for signer_bearer, host_bearer, message in (
            ("", None, "not configured"),
            (TEST_TOKEN, "conflicting-test-token", "does not match"),
        ):
            with self.subTest(message=message):
                self.log.unlink(missing_ok=True)
                result = self.run_reset(signer_bearer, host_bearer)
                self.assertNotEqual(result.returncode, 0)
                self.assertTrue(message in result.stderr)
                self.assertNotIn(TEST_TOKEN, result.stdout + result.stderr)
                self.assertNotIn(
                    "conflicting-test-token", result.stdout + result.stderr
                )
                self.assertEqual(
                    len(self.calls()), 1, "must only read the signer credential"
                )


if __name__ == "__main__":
    unittest.main()
