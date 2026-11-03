#!/usr/bin/env python3
"""Tests for log signatures: python3 -m unittest discover -s tools/ci-health"""

import unittest

import sig

CLONE_FAIL = """#10 4.381 go: gitlab.com/thorchain/tss/tss-lib-private.git/v3@v3.0.0-rc5: no secure protocol found for repository
#10 ERROR: process "/bin/sh -c go mod download" did not complete successfully: exit code: 1
------
Dockerfile.regtest:24
--------------------
ERROR: failed to build: failed to solve: process "/bin/sh -c go mod download"
make: *** [Makefile:333: build-test-regression] Error 1
section_end:1782758885:step_script
section_start:1782758885:cleanup_file_variables
Cleaning up project directory and file based variables
section_end:1782758895:cleanup_file_variables

ERROR: Job failed: exit code 2
"""

HARMLESS = (
    "bifrost/tss/go-tss/common/tss_helper.go:26:2: no secure protocol found for repository\n"
    "bifrost/tss/go-tss/keysign/keysign.go:4:2: no secure protocol found for repository\n"
    + '{"level":"info","message":"setting prefetch blocks"}\n' * 150
    + "ERROR: Job failed: exit code 2\n"
)


class TssTokenTest(unittest.TestCase):
    def test_fetch_error_that_ended_the_job_is_environment(self):
        kind, key, env = sig.signature(CLONE_FAIL)
        self.assertTrue("tss-token" in env)
        self.assertEqual(kind, "unknown")

    def test_fetch_error_the_job_ran_past_is_not_environment(self):
        kind, key, env = sig.signature(HARMLESS)
        self.assertFalse("tss-token" in env)

    def test_error_followed_by_up_to_the_limit_still_counts(self):
        tail = "line\n" * (sig.TSS_FATAL_TAIL_LINES - 1)
        _, _, env = sig.signature(CLONE_FAIL.splitlines()[0] + "\n" + tail)
        self.assertTrue("tss-token" in env)


# go-cmp output as the regression harness prints it (tab-indented)
REG_DIFF = "\n".join(
    [
        "12:19AM ERR cmd/export.go:316 > exports differ:   map[string]any{",
        '  \t"app_state": map[string]any{',
        '  \t\t"thorchain": map[string]any{',
        "  \t\t\t... // 4 identical entries",
        '+ \t\t\t"dynamic_fee_accumulators": []any{},',
        '- \t\t\t"tss_keysign_fail_voters": []any{},',
        '  \t\t\t"last_chain_heights":       []any{},',
        "  \t\t},",
        "  \t},",
        "  }",
        ">> FAIL_FAST: Aborting Now <<",
        "Failed: 1",
        "- suites/savers/savers.yaml",
        "",
    ]
)


class RegressionDiffTest(unittest.TestCase):
    def test_diff_key_names_suite_and_changed_fields(self):
        kind, key, _ = sig.signature(REG_DIFF)
        self.assertEqual(kind, "regression")
        self.assertEqual(
            key,
            "diff:suites/savers/savers.yaml#+dynamic_fee_accumulators,-tss_keysign_fail_voters",
        )

    def test_cross_mr_needs_a_specific_key(self):
        import classify

        self.assertTrue(classify.cross_mr_key("regression", sig.signature(REG_DIFF)[1]))
        self.assertFalse(classify.cross_mr_key("regression", "diff:suites/a.yaml"))
        self.assertFalse(classify.cross_mr_key("sim-node-down", "thornode unreachable"))
        self.assertTrue(classify.cross_mr_key("gotest", "TestFoo"))


class JobLimitTest(unittest.TestCase):
    def test_log_limit_is_not_a_timeout(self):
        _, _, env = sig.signature(
            "Job's log exceeded limit of 4194304 bytes.\nJob execution will continue but no more output will be collected.\n"
        )
        self.assertEqual(env, ["log-limit"])

    def test_timeout(self):
        _, _, env = sig.signature(
            "ERROR: Job failed: execution took longer than 1h0m0s seconds\n"
        )
        self.assertEqual(env, ["job-timeout"])


if __name__ == "__main__":
    unittest.main()
