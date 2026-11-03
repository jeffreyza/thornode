"""Mocknet registration tests using the real shell helpers and JSON parser.

Run with: python3 -m unittest discover -s build/scripts/tests -v
Requires bash, jq and timeout; no chain services or signing keys are used.

The mock thornode and curl commands share a small chain model: an account
sequence, a mempool, blocks produced when the script reads committed state and
a node record changed only by successful transactions. Each transaction must
use the next unused sequence, so resubmitting a registration is visible as a
second committed transaction.
"""

import json
import os
import shutil
import signal
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[1]
REGISTRATIONS = ["deposit", "set-node-keys", "set-ip-address", "set-version"]
ADDRESS = "tthor1mock"
KEYS = {"secp256k1": "mock-node-key", "ed25519": "mock-ed25519"}
VALIDATOR = "mock-cons-pubkey"
IP = "172.19.0.3"
VERSION = "3.19.1"
STARTED = [["render-config"], ["start"]]
SEQUENCE_ERROR = {
    "code": 32,
    "codespace": "sdk",
    "raw_log": "account sequence mismatch",
}

CHAIN = r"""
import json
import os
import signal
from pathlib import Path

root = Path(os.environ["MOCK_STATE"])
plan = json.loads((root / "plan.json").read_text())
state = json.loads((root / "state.json").read_text())


def save():
    (root / "state.json").write_text(json.dumps(state))


def crash():
    # Model a container restart: nothing in the startup process survives.
    # timeout runs this command in its own group, so kill the session's group.
    save()
    os.killpg(os.getsid(0), signal.SIGKILL)
    os._exit(137)


def execute(tx):
    node, account = state["node"], state["account"]
    if tx["sequence"] != account["sequence"]:
        return SEQUENCE_ERROR
    result = tx["spec"].get("execution")
    if result and result["code"] == 32:
        return result  # rejected by the ante handler, so the sequence is unused
    account["sequence"] += 1
    if result:
        return result
    stage, args = tx["stage"], tx["args"]
    if stage == "deposit":
        node["node_operator_address"] = node["node_operator_address"] or tx["signer"]
        node["total_bond"] = str(int(node["total_bond"]) + int(args[0]))
    elif stage == "set-node-keys":
        if node["pub_key_set"]["secp256k1"]:
            return {"code": 99, "codespace": "thorchain", "raw_log": "pubkey set already assigned"}
        node["pub_key_set"] = {"secp256k1": args[0], "ed25519": args[1]}
        node["validator_cons_pub_key"] = args[2]
    elif stage == "set-ip-address":
        node["ip_address"] = args[0]
    elif stage == "set-version":
        node["version"] = os.environ["MOCK_VERSION"]
    state["committed"].append(stage)
    return {"code": 0, "codespace": ""}


def record_block():
    # Keep each height's committed records for queries pinned to that height.
    committed = {"account": state["account"], "node": state["node"]}
    state["history"][str(state["height"])] = json.loads(json.dumps(committed))


def produce_block():
    state["height"] += 1
    waiting = []
    for tx in state["mempool"]:
        if tx["delay"] > 0:
            tx["delay"] -= 1
            waiting.append(tx)
            continue
        result = execute(tx)
        state["transactions"][tx["hash"]].update(result, height=state["height"])
    state["mempool"] = waiting
    record_block()
"""

THORNODE = r"""#!/usr/bin/env python3
import hashlib
import json
import os
import sys

sys.path.insert(0, os.environ["MOCK_STATE"])
from chain import SEQUENCE_ERROR, crash, plan, produce_block, save, state

args = sys.argv[1:]
if args[0] in ("init", "render-config", "start"):
    state["events"].append([args[0]])
    save()
    if args[0] == "render-config" and os.environ.get("FAIL_RENDER"):
        sys.exit(1)
    if args[0] != "start":
        genesis = os.path.join(os.environ["MOCK_STATE"], ".thornode/config/genesis.json")
        if args[0] == "init" and os.path.exists(genesis):
            sys.exit("genesis.json file already exists")
        os.makedirs(os.path.dirname(genesis), exist_ok=True)
        open(genesis, "w").write("{}")
    if args[0] == "init" and os.environ.get("FAIL_INIT"):
        sys.exit(1)
    sys.exit(0)
if args[0] == "keys":
    print("mock-node-key" if "--pubkey" in args else "tthor1mock")
    sys.exit(0)
if args[0] in ("tendermint", "pubkey"):
    print("mock-cons-pubkey" if "cons" in args else "mock-node-key")
    sys.exit(0)
if args[:3] == ["query", "thorchain", "version"]:
    print(json.dumps({"version": os.environ["MOCK_VERSION"]}))
    sys.exit(0)

if args[:2] == ["tx", "thorchain"]:
    if os.environ.get("CHECK_REGISTRATION_STATE"):
        marker = os.path.join(os.environ["MOCK_STATE"], ".thornode/mocknet-registration.state")
        assert open(marker).read() == "registering\n"
    sys.stdin.read()
    stage = args[2]
    stage_args = args[3 : args.index("--node")]
    assert "--offline" in args, "registration must sign with the checked sequence"
    sequence = int(args[args.index("--sequence") + 1])
    assert int(args[args.index("--account-number") + 1]) == state["account"]["number"]
    attempt = state["attempts"].get(stage, 0)
    state["attempts"][stage] = attempt + 1
    responses = plan.get(stage, [{}])
    spec = responses[min(attempt, len(responses) - 1)]
    state["events"].append(["broadcast", stage, args, sequence, list(state["committed"])])
    txhash = hashlib.sha256(json.dumps([stage, stage_args, sequence]).encode()).hexdigest().upper()
    response = {"code": 0, "codespace": "", "height": "0", "txhash": txhash}
    pending = state["account"]["sequence"] + len(state["mempool"])
    if any(tx["hash"] == txhash for tx in state["mempool"]):
        response.update(code=19, codespace="sdk", raw_log="tx already exists in cache")
    elif sequence != pending:
        response.update(SEQUENCE_ERROR)
    response.update(spec.get("broadcast", {}))
    accepted = spec.get("accepted", response["code"] == 0 and "exit" not in spec and "raw" not in spec)
    if accepted:
        state["transactions"][txhash] = {"indexed": spec.get("index", True)}
        state["mempool"].append({
            "hash": txhash, "stage": stage, "args": stage_args, "sequence": sequence,
            "signer": "tthor1mock", "delay": spec.get("delay", 0), "spec": spec,
        })
        if spec.get("commit_now"):
            produce_block()
    if spec.get("crash"):
        crash()
elif args[:2] == ["query", "tx"]:
    produce_block()
    tx = state["transactions"].get(args[2])
    spec = {}
    if tx is None or "height" not in tx or not tx["indexed"]:
        spec = {"exit": 1, "raw": "tx not found"}
    else:
        response = {"txhash": args[2], "height": str(tx["height"]), "code": tx["code"],
                    "codespace": tx.get("codespace", ""), "raw_log": tx.get("raw_log", "")}
    state["events"].append(["query", args])
else:
    raise AssertionError(f"unexpected command: {args}")

save()
print(spec.get("raw", json.dumps(response)))
sys.exit(spec.get("exit", 0))
"""

CURL = r"""#!/usr/bin/env python3
import json
import os
import sys
from urllib.parse import urlparse, parse_qs

sys.path.insert(0, os.environ["MOCK_STATE"])
from chain import plan, produce_block, record_block, save, state

url = next((a for a in sys.argv[1:] if a.startswith("http://")), None)
if url is None or "whatismyip" in url:
    sys.exit(0)  # peer readiness probe
parsed = urlparse(url)
path = parsed.path
assert parsed.netloc == "mock-peer:1317", url
height = parse_qs(parsed.query).get("height", [None])[0]

failures = plan.get("api_failures", {})
for prefix, count in failures.items():
    used = state["api_failures"].get(prefix, 0)
    if path.startswith(prefix) and (count < 0 or used < count):
        state["api_failures"][prefix] = used + 1
        save()
        sys.exit(22)

if not state["history"]:
    record_block()
if height is None:
    produce_block()
    served = state["height"]
    # An API node that lags the RPC node serves older state after a commit.
    if state["committed"] and state["api_lag"] < plan.get("api_lag", 0):
        state["api_lag"] += 1
        served -= 2
else:
    served = int(height)
committed = state["history"][str(served)]
state["events"].append(["api", path, height, served])

if path == "/cosmos/auth/v1beta1/accounts/tthor1mock":
    account = committed["account"]
    body = {"account": {"address": "tthor1mock", "account_number": str(account["number"]),
                        "sequence": str(account["sequence"])}}
elif path == "/thorchain/node/tthor1mock":
    body = dict(committed["node"], node_address="tthor1mock")
elif path == "/thorchain/nodes":
    body = state["other_nodes"] + [dict(committed["node"], node_address="tthor1mock")]
else:
    raise AssertionError(f"unexpected query: {url}")
save()
header = r"\n%header{grpc-metadata-x-cosmos-block-height}"
assert sys.argv[sys.argv.index("-w") + 1] == header, sys.argv
sys.stdout.write(json.dumps(body) + "\n" + str(served))
"""


def empty_node():
    return {
        "node_operator_address": "",
        "total_bond": "0",
        "pub_key_set": {"secp256k1": "", "ed25519": ""},
        "validator_cons_pub_key": "",
        "ip_address": "",
        "version": "0.0.0",
    }


def registered_node(**changes):
    node = {
        "node_operator_address": ADDRESS,
        "total_bond": "100000000000000",
        "pub_key_set": dict(KEYS),
        "validator_cons_pub_key": VALIDATOR,
        "ip_address": IP,
        "version": VERSION,
    }
    node.update(changes)
    return node


class MocknetRegistrationTest(unittest.TestCase):
    def setUp(self):
        for tool in ("bash", "jq", "timeout"):
            self.assertIsNotNone(
                shutil.which(tool), f"required test tool missing: {tool}"
            )
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "chain.py").write_text(
            f"{CHAIN}\nSEQUENCE_ERROR = {SEQUENCE_ERROR!r}\n"
        )
        self.write_executable("thornode", THORNODE)
        self.write_executable("curl", CURL)
        self.write_executable("sleep", "#!/bin/sh\nexit 0\n")
        self.write_executable("hostname", f"#!/bin/sh\necho {IP}\n")
        self.env = {
            **os.environ,
            "PATH": str(self.root) + os.pathsep + os.environ["PATH"],
            "MOCK_STATE": str(self.root),
            "MOCK_VERSION": VERSION,
            "NET": "test",
            "PEER": "mock-peer",
            "PORT_RPC": "26699",
            "SIGNER_PASSWD": "test-only",
            "SIGNER_NAME": "thorchain",
            "CHAIN_ID": "thorchain",
            "NODE_PUB_KEY_ED25519": KEYS["ed25519"],
            "EXTERNAL_IP": IP,
            "HOME": str(self.root),
        }

    def assert_contains(self, item, container):
        self.assertTrue(item in container, f"{item!r} not found in {container!r}")

    def write_executable(self, name, content):
        path = self.root / name
        path.write_text(content)
        path.chmod(0o755)

    def initialize_state(self, plan=None, node=None, other_nodes=()):
        self.set_plan(plan or {})
        (self.root / "state.json").write_text(
            json.dumps(
                {
                    "height": 10,
                    "account": {"number": 7, "sequence": 0},
                    "node": node or empty_node(),
                    "other_nodes": list(other_nodes),
                    "mempool": [],
                    "transactions": {},
                    "attempts": {},
                    "api_failures": {},
                    "api_lag": 0,
                    "history": {},
                    "events": [],
                    "committed": [],
                }
            )
        )

    def set_plan(self, plan):
        (self.root / "plan.json").write_text(json.dumps(plan))

    def state(self):
        return json.loads((self.root / "state.json").read_text())

    def run_startup(self, plan, **chain):
        self.initialize_state(plan, **chain)
        script = os.environ.get("CORE_UNSAFE_SCRIPT", str(SCRIPTS / "core-unsafe.sh"))
        result = subprocess.run(
            ["bash", "-c", 'source "$1"; init_mocknet', "test", script],
            env=self.env,
            capture_output=True,
            text=True,
            timeout=60,
            start_new_session=True,
        )
        return result, self.state()

    def assert_fully_registered(self, state):
        self.assertEqual(state["node"], registered_node())
        self.assertEqual(sorted(state["committed"]), sorted(REGISTRATIONS))
        self.assertEqual(state["mempool"], [])

    def broadcasts(self, state):
        return [e for e in state["events"] if e[0] == "broadcast"]

    def test_fresh_registration_commits_each_step_once_in_order(self):
        result, state = self.run_startup({})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_fully_registered(state)
        self.assertEqual(state["committed"], REGISTRATIONS)
        for i, event in enumerate(self.broadcasts(state)):
            _, stage, args, sequence, committed_before = event
            self.assertEqual(stage, REGISTRATIONS[i])
            self.assertEqual(sequence, i)
            self.assertEqual(committed_before, REGISTRATIONS[:i])
            self.assert_contains("tcp://mock-peer:26699", args)
            self.assert_contains("sync", args)
            self.assert_contains("json", args)
        # Each snapshot pins its node queries to the account query's height,
        # and only the keys step reads every node.
        snapshot_height, node_lists = None, 0
        for _, path, height, served in (e for e in state["events"] if e[0] == "api"):
            if path.startswith("/cosmos/auth/"):
                self.assertIsNone(height)
                snapshot_height = served
            else:
                self.assertEqual(height, str(snapshot_height))
                node_lists += path == "/thorchain/nodes"
        self.assertEqual(node_lists, 2)

    def test_already_registered_node_sends_nothing(self):
        result, state = self.run_startup({}, node=registered_node())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.broadcasts(state), [])
        self.assertEqual(result.stdout.count("registration is recorded"), 4)

    def test_partial_registration_sends_only_missing_steps(self):
        partial = {
            "deposit": registered_node(
                pub_key_set={"secp256k1": "", "ed25519": ""},
                validator_cons_pub_key="",
                ip_address="",
                version="0.0.0",
            ),
            "set-node-keys": registered_node(ip_address="", version="0.0.0"),
            "set-ip-address": registered_node(version="0.0.0"),
        }
        for last, node in partial.items():
            with self.subTest(completed=last):
                result, state = self.run_startup({}, node=node)
                self.assertEqual(result.returncode, 0, result.stderr)
                completed = REGISTRATIONS.index(last) + 1
                remaining = REGISTRATIONS[completed:]
                self.assertEqual([e[1] for e in self.broadcasts(state)], remaining)
                self.assertEqual(state["node"], registered_node())

    def test_updates_a_changed_ip_address_and_accepts_a_newer_version(self):
        node = registered_node(ip_address="172.19.0.9", version="3.20.0")
        result, state = self.run_startup({}, node=node)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([e[1] for e in self.broadcasts(state)], ["set-ip-address"])
        self.assertEqual(state["node"]["ip_address"], IP)

    def test_commit_before_client_learns_success_is_not_resent(self):
        # The chain executes the transaction, but the client never sees a
        # result: the CLI fails after acceptance or the index never finds it.
        for outcome in (
            {"accepted": True, "exit": 1, "raw": "EOF"},
            {"accepted": True, "raw": "invalid json"},
            {"index": False},
        ):
            with self.subTest(outcome=outcome):
                plan = {stage: [outcome] for stage in REGISTRATIONS}
                result, state = self.run_startup(plan)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(state["attempts"], dict.fromkeys(REGISTRATIONS, 1))
                self.assert_fully_registered(state)
                self.assertEqual(state["committed"], REGISTRATIONS)

    def test_pending_transaction_is_not_duplicated(self):
        # The first bond waits in the mempool through several state reads.
        # Retries sign with the same sequence, so only one bond can execute.
        result, state = self.run_startup({"deposit": [{"delay": 35}, {}]})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertGreater(state["attempts"]["deposit"], 1)
        self.assertEqual(state["committed"].count("deposit"), 1)
        self.assert_fully_registered(state)

    def test_waits_for_a_lagging_api_after_a_commit(self):
        result, state = self.run_startup({"api_lag": 3})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state["api_lag"], 3)
        self.assertEqual(state["attempts"], {stage: 1 for stage in REGISTRATIONS})
        self.assert_fully_registered(state)

    def test_committed_transaction_without_the_intended_record_stops(self):
        silent = {"execution": {"code": 0, "codespace": ""}}
        for stage in REGISTRATIONS:
            with self.subTest(stage=stage):
                result, state = self.run_startup({stage: [silent]})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(state["attempts"][stage], 1)
                self.assert_contains("does not match the intended setup", result.stderr)

    def test_sequence_used_by_another_transaction_is_rechecked(self):
        for phase in ("broadcast", "execution"):
            with self.subTest(phase=phase):
                plan = {stage: [{phase: SEQUENCE_ERROR}, {}] for stage in REGISTRATIONS}
                result, state = self.run_startup(plan)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(state["attempts"], dict.fromkeys(REGISTRATIONS, 2))
                self.assert_fully_registered(state)
                for stage in REGISTRATIONS:
                    retry = [e for e in self.broadcasts(state) if e[1] == stage]
                    self.assertEqual(retry[0][3], retry[1][3])

    def test_bounds_retries_when_the_outcome_stays_unknown(self):
        for spec in ({"broadcast": SEQUENCE_ERROR}, {"exit": 1}, {"delay": 10**6}):
            with self.subTest(spec=spec):
                result, state = self.run_startup({"set-ip-address": [spec]})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(state["attempts"]["set-ip-address"], 10)
                self.assertNotIn("set-version", state["attempts"])
                self.assert_contains(
                    "was not recorded after 10 attempts", result.stderr
                )

    def test_stops_on_permanent_rejection(self):
        rejection = {"code": 5, "codespace": "sdk", "raw_log": "insufficient funds"}
        for spec in (
            {"broadcast": rejection},
            {"broadcast": {"code": 32, "codespace": "other"}},
            {"execution": rejection},
        ):
            with self.subTest(spec=spec):
                result, state = self.run_startup({"deposit": [spec]})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(state["attempts"], {"deposit": 1})
                self.assert_contains("rejected", result.stderr)

    def test_conflicting_state_is_reported_without_sending(self):
        other = registered_node(
            node_address="tthor1other", node_operator_address="tthor1other"
        )
        cases = {
            "operated by tthor1other": {
                "node": registered_node(node_operator_address="tthor1other")
            },
            "already has keys other-key": {
                "node": registered_node(
                    pub_key_set={"secp256k1": "other-key", "ed25519": ""},
                    ip_address="",
                )
            },
            "already registered to tthor1other": {
                "node": registered_node(
                    pub_key_set={"secp256k1": "", "ed25519": ""},
                    validator_cons_pub_key="",
                ),
                "other_nodes": [other],
            },
        }
        for message, chain in cases.items():
            with self.subTest(message=message):
                result, state = self.run_startup({}, **chain)
                self.assertNotEqual(result.returncode, 0)
                self.assert_contains("conflicts with committed state", result.stderr)
                self.assert_contains(message, result.stderr)
                self.assertEqual(self.broadcasts(state), [])

    def test_retries_temporary_query_failures(self):
        for prefix in ("/cosmos/auth", "/thorchain/node/", "/thorchain/nodes"):
            with self.subTest(prefix=prefix):
                result, state = self.run_startup({"api_failures": {prefix: 9}})
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assert_fully_registered(state)

    def test_bounds_query_failures_before_sending(self):
        # The node list is read only before registering keys.
        for prefix, sent in (
            ("/cosmos/auth", []),
            ("/thorchain/node/", []),
            ("/thorchain/nodes", ["deposit"]),
        ):
            with self.subTest(prefix=prefix):
                result, state = self.run_startup({"api_failures": {prefix: -1}})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual([e[1] for e in self.broadcasts(state)], sent)
                self.assert_contains("failed after 10 attempts", result.stderr)
                self.assertEqual(state["api_failures"][prefix], 10)

    def test_missing_ip_address_stops_registration(self):
        self.env["EXTERNAL_IP"] = ""
        result, state = self.run_startup({})
        self.assertNotEqual(result.returncode, 0)
        self.assert_contains("node keys or IP address", result.stderr)
        self.assertEqual(self.broadcasts(state), [])

    def test_invalid_local_version_stops_registration(self):
        self.env["MOCK_VERSION"] = "3.19.1-rc1"
        result, state = self.run_startup({})
        self.assertNotEqual(result.returncode, 0)
        self.assert_contains("local THORNode version", result.stderr)
        self.assertEqual(self.broadcasts(state), [])

    # ---------------------------------------------------------------- validator

    def prepare_validator(self, plan=None, **chain):
        self.initialize_state(plan, **chain)
        validator = os.environ.get("VALIDATOR_SCRIPT", str(SCRIPTS / "validator.sh"))
        shutil.copyfile(validator, self.root / "validator.sh")
        shutil.copyfile(SCRIPTS / "core-unsafe.sh", self.root / "core-unsafe.sh")
        (self.root / "core.sh").write_text(
            "resolve_signer_seed_phrase() { :; }\n"
            "create_thor_user() { :; }\n"
            # Like the real init_chain, leave IFS set to a comma.
            'init_chain() { IFS=","; thornode init; }\n'
        )
        self.marker = self.root / ".thornode/mocknet-registration.state"
        self.genesis = self.root / ".thornode/config/genesis.json"

    def run_validator(self, **env):
        result = subprocess.run(
            ["bash", str(self.root / "validator.sh")],
            env={**self.env, "NET": "mocknet", "CHECK_REGISTRATION_STATE": "1", **env},
            capture_output=True,
            text=True,
            timeout=60,
            start_new_session=True,
        )
        return result, self.state()

    def events_since(self, before, after):
        previous = len(before["events"])
        return after["events"][previous:]

    def test_validator_registers_once_and_restarts_normally(self):
        self.prepare_validator()
        first, before = self.run_validator()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(before["committed"], REGISTRATIONS)
        self.assertEqual(self.marker.read_text(), "registered\n")
        self.assertTrue(self.genesis.exists())
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["attempts"], before["attempts"])
        self.assertEqual(self.events_since(before, after), STARTED)

    def test_validator_resumes_after_a_restart_at_each_step(self):
        # The container dies right after each transaction commits, before the
        # client learns the result. Startup then resumes from chain state.
        for stage in REGISTRATIONS:
            with self.subTest(stage=stage):
                self.prepare_validator({stage: [{"commit_now": True, "crash": True}]})
                first, before = self.run_validator()
                self.assertEqual(first.returncode, -signal.SIGKILL)
                self.assertEqual(self.marker.read_text(), "registering\n")
                self.assertEqual(before["committed"][-1], stage)
                self.set_plan({})
                second, after = self.run_validator()
                self.assertEqual(second.returncode, 0, second.stderr)
                self.assert_contains(
                    "Resuming incomplete mocknet registration", second.stdout
                )
                self.assertEqual(self.marker.read_text(), "registered\n")
                self.assertEqual(after["attempts"][stage], 1)
                self.assertEqual(after["committed"], REGISTRATIONS)
                self.assert_fully_registered(after)
                self.assertEqual(self.events_since(before, after)[-2:], STARTED)
                shutil.rmtree(self.root / ".thornode")

    def test_validator_restart_with_pending_bond_does_not_bond_twice(self):
        self.prepare_validator({"deposit": [{"crash": True, "delay": 3}]})
        first, before = self.run_validator()
        self.assertEqual(first.returncode, -signal.SIGKILL)
        self.assertEqual(before["committed"], [])
        self.assertEqual(len(before["mempool"]), 1)
        self.set_plan({})
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["committed"].count("deposit"), 1)
        self.assert_fully_registered(after)

    def test_validator_resumes_after_a_rejected_step(self):
        self.prepare_validator({"set-node-keys": [{"broadcast": {"code": 5}}]})
        first, before = self.run_validator()
        self.assertNotEqual(first.returncode, 0)
        self.assertEqual(before["committed"], ["deposit"])
        self.assertEqual(self.marker.read_text(), "registering\n")
        self.set_plan({})
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["attempts"]["deposit"], 1)
        self.assert_fully_registered(after)

    def test_validator_resumes_when_registration_finished_before_marker(self):
        self.prepare_validator()
        self.write_executable(
            "mv",
            '#!/bin/sh\nif [ "$(cat "$2")" = registered ]; then exit 1; fi\n'
            'exec /bin/mv "$@"\n',
        )
        first, before = self.run_validator()
        self.assertNotEqual(first.returncode, 0)
        self.assertEqual(before["committed"], REGISTRATIONS)
        self.assertEqual(self.marker.read_text(), "registering\n")
        self.assertNotIn(["render-config"], before["events"])
        (self.root / "mv").unlink()
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["attempts"], before["attempts"])
        self.assertEqual(self.marker.read_text(), "registered\n")
        self.assertEqual(self.events_since(before, after)[-2:], STARTED)

    def test_validator_stops_on_conflicting_chain_state(self):
        self.prepare_validator(
            node=registered_node(node_operator_address="tthor1other")
        )
        result, state = self.run_validator()
        self.assertNotEqual(result.returncode, 0)
        self.assert_contains("operated by tthor1other", result.stderr)
        self.assertEqual(self.broadcasts(state), [])
        self.assertEqual(self.marker.read_text(), "registering\n")
        self.assertNotIn(["start"], state["events"])

    def test_validator_retries_initialization_after_failed_init(self):
        self.prepare_validator()
        first, before = self.run_validator(FAIL_INIT="1")
        self.assertNotEqual(first.returncode, 0)
        self.assertTrue(self.genesis.exists())
        self.assertEqual(before["attempts"], {})
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["committed"], REGISTRATIONS)

    def test_validator_resumes_rendering_after_confirmed_registration(self):
        self.prepare_validator()
        first, before = self.run_validator(FAIL_RENDER="1")
        self.assertNotEqual(first.returncode, 0)
        self.assertFalse(self.genesis.exists())
        self.assertEqual(self.marker.read_text(), "registered\n")
        self.assertEqual(before["committed"], REGISTRATIONS)
        self.assertNotIn(["start"], before["events"])
        second, after = self.run_validator()
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(after["attempts"], before["attempts"])
        self.assertEqual(self.events_since(before, after), STARTED)

    def test_validator_stops_if_initial_marker_cannot_be_persisted(self):
        self.prepare_validator()
        self.write_executable("mv", "#!/bin/sh\nexit 1\n")
        result, state = self.run_validator()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state["events"], [])
        self.assertFalse(self.marker.exists())

    def test_validator_refuses_invalid_registration_state(self):
        self.prepare_validator()
        self.marker.parent.mkdir()
        self.marker.write_text("invalid\n")
        result, state = self.run_validator()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state["events"], [])

    def test_validator_preserves_existing_initialized_volume_without_marker(self):
        self.prepare_validator()
        self.genesis.parent.mkdir(parents=True)
        self.genesis.write_text("{}")
        result, state = self.run_validator()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state["attempts"], {})
        self.assertEqual(state["events"], [["render-config"], ["start"]])
        self.assertFalse(self.marker.exists())


if __name__ == "__main__":
    unittest.main()
