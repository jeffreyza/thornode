"""Readiness tests without the unrelated Web3 deployment dependencies."""

import ast
import errno
import logging
import socket
import time
import types
import unittest
from contextlib import closing
from pathlib import Path
from unittest.mock import Mock, call, patch
from urllib.parse import urlparse

SCRIPT = Path(__file__).resolve().parents[1] / "evm" / "evm-tool.py"


def load_readiness():
    # Execute the real helpers, isolating them from contract deployment imports.
    tree = ast.parse(SCRIPT.read_text())
    tree.body = [
        node
        for node in tree.body
        if isinstance(node, ast.FunctionDef)
        and node.name
        in {
            "check_socket",
            "wait_for_rpc_open",
            "wait_for_block_number",
            "wait_for_chain_progress",
        }
    ]
    module = types.ModuleType("evm_readiness")
    module.__dict__.update(
        socket=socket,
        closing=closing,
        time=time,
        urlparse=urlparse,
        logging=logging,
        rpc_call=Mock(),
    )
    exec(compile(tree, str(SCRIPT), "exec"), module.__dict__)
    return module


class EVMReadinessTest(unittest.TestCase):
    def setUp(self):
        self.readiness = load_readiness()

    def test_transient_lookup_and_connection_errors_recover(self):
        errors = [
            socket.gaierror(socket.EAI_AGAIN, "Temporary failure in name resolution"),
            OSError(errno.ENETUNREACH, "Network is unreachable"),
            socket.timeout("connect timed out"),
            errno.ECONNREFUSED,
            0,
        ]
        sockets = [Mock() for _ in errors]
        for sock, result in zip(sockets, errors):
            if isinstance(result, Exception):
                sock.connect_ex.side_effect = result
            else:
                sock.connect_ex.return_value = result
        with patch.object(socket, "socket", side_effect=sockets), patch.object(
            time, "sleep"
        ) as sleep:
            self.readiness.wait_for_rpc_open(
                "http://avalanche:9650/ext/bc/C/rpc", attempts=5
            )
        self.assertEqual(sleep.call_count, 4)
        for sock in sockets:
            sock.connect_ex.assert_called_once_with(("avalanche", 9650))
            sock.settimeout.assert_called_once_with(3)
            sock.close.assert_called_once()

    def test_persistent_dns_failure_exhausts_retries_and_preserves_cause(self):
        error = socket.gaierror(socket.EAI_AGAIN, "Temporary DNS failure")
        with patch.object(
            self.readiness, "check_socket", side_effect=error
        ) as check, patch.object(time, "sleep") as sleep:
            with self.assertRaisesRegex(TimeoutError, "polygon:8545") as caught:
                self.readiness.wait_for_rpc_open(
                    "http://polygon:8545", attempts=3, interval=2
                )
        self.assertIs(caught.exception.__cause__, error)
        self.assertEqual(check.call_count, 3)
        self.assertEqual(sleep.call_args_list, [call(2)] * 2)

    def test_closed_port_exhausts_retries(self):
        with patch.object(
            self.readiness, "check_socket", return_value=False
        ) as check, patch.object(time, "sleep") as sleep:
            with self.assertRaisesRegex(TimeoutError, "polygon:8545"):
                self.readiness.wait_for_rpc_open("http://polygon:8545", attempts=2)
        self.assertEqual(check.call_count, 2)
        sleep.assert_called_once_with(1)

    def test_ready_socket_does_not_sleep(self):
        with patch.object(
            self.readiness, "check_socket", return_value=True
        ) as check, patch.object(time, "sleep") as sleep:
            self.readiness.wait_for_rpc_open("http://polygon:8545")
        check.assert_called_once()
        sleep.assert_not_called()

    def test_closed_port_clears_previous_dns_error(self):
        error = socket.gaierror(socket.EAI_AGAIN, "Temporary DNS failure")
        with patch.object(
            self.readiness, "check_socket", side_effect=[error, False, False]
        ) as check, patch.object(time, "sleep") as sleep:
            with self.assertRaisesRegex(TimeoutError, "polygon:8545") as caught:
                self.readiness.wait_for_rpc_open("http://polygon:8545", attempts=3)
        self.assertIsNone(caught.exception.__cause__)
        self.assertEqual(check.call_count, 3)
        self.assertEqual(sleep.call_args_list, [call(1)] * 2)

    def test_chain_progress_recovers_after_transient_rpc_errors(self):
        rpc = self.readiness.rpc_call
        rpc.side_effect = [
            "0x100",
            socket.gaierror(socket.EAI_AGAIN, "Temporary DNS failure"),
            OSError(errno.ECONNRESET, "Connection reset"),
            "0x100",
            "0x101",
        ]
        with patch.object(time, "sleep") as sleep:
            block = self.readiness.wait_for_chain_progress(
                "http://polygon:8545", attempts=4, interval=2
            )
        self.assertEqual(block, 257)
        self.assertEqual(
            rpc.call_args_list, [call("http://polygon:8545", "eth_blockNumber")] * 5
        )
        self.assertEqual(sleep.call_args_list, [call(2)] * 4)

    def test_chain_progress_rpc_failures_exhaust_poll_budget(self):
        error = OSError(errno.ECONNRESET, "Connection reset")
        rpc = self.readiness.rpc_call
        rpc.side_effect = ["0x100", error, error, error]
        with patch.object(time, "sleep") as sleep:
            with self.assertRaisesRegex(
                TimeoutError, "block production on http://polygon:8545 from block 256"
            ) as caught:
                self.readiness.wait_for_chain_progress(
                    "http://polygon:8545", attempts=3, interval=2
                )
        self.assertIs(caught.exception.__cause__.__cause__, error)
        self.assertEqual(rpc.call_count, 4)
        self.assertEqual(sleep.call_args_list, [call(2)] * 3)

    def test_chain_progress_times_out_when_blocks_do_not_advance(self):
        for first_poll in [
            "0x100",
            OSError(errno.ECONNRESET, "Connection reset"),
        ]:
            with self.subTest(first_poll=first_poll):
                rpc = self.readiness.rpc_call
                rpc.reset_mock()
                rpc.side_effect = ["0x100", first_poll, "0xff", "0x100"]
                with patch.object(time, "sleep") as sleep:
                    with self.assertRaisesRegex(
                        TimeoutError,
                        "block production on http://polygon:8545 from block 256",
                    ) as caught:
                        self.readiness.wait_for_chain_progress(
                            "http://polygon:8545", attempts=3, interval=2
                        )
                self.assertIsNone(caught.exception.__cause__)
                self.assertEqual(rpc.call_count, 4)
                self.assertEqual(sleep.call_args_list, [call(2)] * 3)

    def test_programming_errors_are_not_retried(self):
        with patch.object(
            self.readiness, "check_socket", side_effect=ValueError("bad port")
        ) as check, patch.object(time, "sleep") as sleep:
            with self.assertRaisesRegex(ValueError, "bad port"):
                self.readiness.wait_for_rpc_open("http://polygon:8545")
        check.assert_called_once()
        sleep.assert_not_called()


if __name__ == "__main__":
    unittest.main()
