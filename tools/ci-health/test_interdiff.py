#!/usr/bin/env python3
"""Tests for interdiffs: python3 -m unittest discover -s tools/ci-health"""

import os
import subprocess
import tempfile
import unittest

import interdiff


class MergedMRTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = self.tmp.name
        self.git("init", "--quiet", "-b", "develop")
        self.commit("base.txt", "base")

    def tearDown(self):
        self.tmp.cleanup()

    def git(self, *a):
        env = dict(
            os.environ,
            GIT_AUTHOR_NAME="t",
            GIT_AUTHOR_EMAIL="t@t",
            GIT_COMMITTER_NAME="t",
            GIT_COMMITTER_EMAIL="t@t",
        )
        r = subprocess.run(
            ["git", "-C", self.dir] + list(a),
            capture_output=True,
            text=True,
            check=True,
            env=env,
        )
        return r.stdout.strip()

    def commit(self, name, body):
        with open(os.path.join(self.dir, name), "w") as f:
            f.write(body + "\n")
        self.git("add", name)
        self.git("commit", "--quiet", "-m", name)
        return self.git("rev-parse", "HEAD")

    def mirror(self):
        return interdiff.Mirror(self.dir, "refs/heads/develop")

    def test_fix_counts_after_a_merge_commit(self):
        self.git("checkout", "--quiet", "-b", "mr")
        red = self.commit("code.go", "broken")
        green = self.commit("code.go", "fixed")
        self.git("checkout", "--quiet", "develop")
        self.commit("other.txt", "develop moved")
        self.git("merge", "--quiet", "--no-ff", "-m", "merge mr", "mr")
        self.commit("later.txt", "after the merge")
        _, r = interdiff.analyse(self.mirror(), (red, green))
        self.assertEqual(r["status"], "ok")
        self.assertEqual(r["files"], ["code.go"])

    def test_fast_forward_is_unknown(self):
        self.git("checkout", "--quiet", "-b", "mr")
        red = self.commit("code.go", "broken")
        green = self.commit("code.go", "fixed")
        self.git("checkout", "--quiet", "develop")
        self.git("merge", "--quiet", "--ff-only", "mr")
        _, r = interdiff.analyse(self.mirror(), (red, green))
        self.assertEqual(r["status"], "merged")

    def test_unmerged_uses_develop(self):
        self.git("checkout", "--quiet", "-b", "mr")
        red = self.commit("code.go", "broken")
        green = self.commit("code.go", "fixed")
        self.git("checkout", "--quiet", "develop")
        self.commit("other.txt", "develop moved")
        _, r = interdiff.analyse(self.mirror(), (red, green))
        self.assertEqual(r["status"], "ok")
        self.assertEqual(r["files"], ["code.go"])

    def test_rebase_past_a_nearby_develop_edit_is_no_change(self):
        lines = [f"line {i}" for i in range(10)]
        self.commit("code.go", "\n".join(lines))
        self.git("checkout", "--quiet", "-b", "mr")
        lines[4] = "mr change"
        red = self.commit("code.go", "\n".join(lines))
        self.git("checkout", "--quiet", "develop")
        dev = lines[:4] + ["line 4"] + lines[5:]
        dev[6] = "develop change"
        self.commit("code.go", "\n".join(dev))
        self.git("checkout", "--quiet", "-b", "rebased")
        dev[4] = "mr change"
        green = self.commit("code.go", "\n".join(dev))
        _, r = interdiff.analyse(self.mirror(), (red, green))
        self.assertEqual(r["status"], "ok")
        self.assertTrue(r["rebased"])
        self.assertEqual(r["files"], [])


if __name__ == "__main__":
    unittest.main()
