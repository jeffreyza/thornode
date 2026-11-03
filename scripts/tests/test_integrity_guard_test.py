import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "test-integrity-guard.py"

TEST_A = "func TestA(t *testing.T) {\n\tt.Log(1)\n}\n\n"
BASE_FILES = {
    "x/a_test.go": "package x\n\nfunc TestMain(m *testing.M) {}\n\n"
    + TEST_A
    + "func (s *ASuite) TestB(c *C) {}\n",
    "x/b_test.go": "package x\n\nfunc TestC(t *testing.T) {\n\t// keep\n}\n",
    "test/regression/suites/core/x.yaml": "type: env\n",
    "test/regression/suites/core/y.yaml": "type: env\n",
    "test/regression/mnt/exports/core/x.json": "{}\n",
}


class IntegrityGuardTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.git("init", "-q")
        self.git("config", "user.email", "test@example.com")
        self.git("config", "user.name", "Test")
        for path, text in BASE_FILES.items():
            self.write(path, text)
        self.base = self.commit("base")

    def tearDown(self):
        self.tmp.cleanup()

    def git(self, *args):
        return subprocess.run(
            ["git", *args], cwd=self.root, check=True, capture_output=True, text=True
        ).stdout.strip()

    def write(self, path, text):
        (self.root / path).parent.mkdir(parents=True, exist_ok=True)
        (self.root / path).write_text(text)

    def commit(self, message):
        self.git("add", "-A")
        self.git("commit", "-qm", message)
        return self.git("rev-parse", "HEAD")

    def guard(self, labels=""):
        head = self.commit("change")
        env = {k: v for k, v in os.environ.items() if not k.startswith("CI_")}
        return subprocess.run(
            [sys.executable, str(SCRIPT), "--base", self.base, "--head", head],
            cwd=self.root,
            env={**env, "CI_MERGE_REQUEST_LABELS": labels},
            capture_output=True,
            text=True,
        )

    def test_moving_and_renaming_tests_passes(self):
        text = BASE_FILES["x/a_test.go"].replace(TEST_A, "")
        self.write("x/a_test.go", text.replace(") TestB(", ") TestBRenamed("))
        self.write("x/c_test.go", "package x\n\n" + TEST_A)
        self.git("mv", "x/b_test.go", "x/d_test.go")
        self.git(
            "mv", "test/regression/suites/core/y.yaml", "test/regression/suites/y.yaml"
        )
        result = self.guard()
        self.assertEqual(result.returncode, 0, result.stdout)

    def test_removing_test_main_passes(self):
        self.write(
            "x/a_test.go",
            BASE_FILES["x/a_test.go"].replace("func TestMain(m *testing.M) {}\n", ""),
        )
        self.assertEqual(self.guard().returncode, 0)

    def test_deleted_test_file_and_net_test_loss_fail(self):
        (self.root / "x/b_test.go").unlink()
        # A rename does not offset a removal: two tests in, one out.
        self.write(
            "x/a_test.go",
            BASE_FILES["x/a_test.go"]
            .replace("func (s *ASuite) TestB(c *C) {}\n", "")
            .replace("TestA(", "TestARenamed("),
        )
        result = self.guard()
        self.assertEqual(result.returncode, 1)
        self.assertTrue(
            "Deleted test files (*_test.go):\n  x/b_test.go\n" in result.stdout,
            result.stdout,
        )
        self.assertTrue(
            "  x/a_test.go: 2 -> 1 tests, gone: func TestA, func (s *ASuite) TestB\n"
            in result.stdout,
            result.stdout,
        )
        self.assertFalse("TestC" in result.stdout, result.stdout)
        self.assertFalse("TestMain" in result.stdout, result.stdout)
        self.assertTrue(
            "add the label goldens-reviewed" in result.stdout, result.stdout
        )

    def test_test_file_moved_where_go_skips_it_fails(self):
        for skipped in ("testdata", "_old", ".old"):
            with self.subTest(skipped):
                (self.root / "x" / skipped).mkdir()
                self.git("mv", "x/b_test.go", f"x/{skipped}/b_test.go")
                result = self.guard()
                self.assertEqual(result.returncode, 1, result.stdout)
                self.assertTrue(
                    f"  x/b_test.go -> x/{skipped}/b_test.go\n" in result.stdout,
                    result.stdout,
                )
                self.git("reset", "-q", "--hard", self.base)

    def test_goldens_pass_only_when_added_with_a_new_suite(self):
        self.write("test/regression/suites/core/z.yaml", "type: env\n")
        self.write("test/regression/mnt/exports/core/z.json", "{}\n")
        self.write("test/regression/mnt/blocks/core/z.json", "[]\n")
        result = self.guard()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertTrue(
            "2 goldens added with new regression suites" in result.stdout,
            result.stdout,
        )
        self.write("test/regression/mnt/exports/core/y.json", "{}\n")
        result = self.guard()
        self.assertEqual(result.returncode, 1, result.stdout)
        self.assertTrue(
            "  A test/regression/mnt/exports/core/y.json\n" in result.stdout,
            result.stdout,
        )
        self.assertFalse("core/z.json" in result.stdout, result.stdout)

    def test_disabled_suite_and_changed_golden_fail_until_labelled(self):
        self.git(
            "mv",
            "test/regression/suites/core/y.yaml",
            "test/regression/suites/core/y.yaml.disabled",
        )
        self.write("test/regression/mnt/exports/core/x.json", '{"changed": true}\n')
        result = self.guard()
        self.assertEqual(result.returncode, 1)
        self.assertTrue(
            "  test/regression/suites/core/y.yaml -> test/regression/suites/core/y.yaml.disabled\n"
            in result.stdout,
            result.stdout,
        )
        self.assertTrue(
            "  M test/regression/mnt/exports/core/x.json\n" in result.stdout,
            result.stdout,
        )
        self.git("reset", "-q", "--soft", "HEAD~1")
        result = self.guard(labels="priority::3,goldens-reviewed")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertTrue(
            "M test/regression/mnt/exports/core/x.json" in result.stdout, result.stdout
        )


if __name__ == "__main__":
    unittest.main()
