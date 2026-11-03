#!/usr/bin/env python3
"""Tests for the fetch stage: python3 -m unittest discover -s tools/ci-health"""

import json
import os
import tempfile
import unittest
import urllib.error
import urllib.parse

import fetch
from gitlab import Response


class FakeGitLab:
    api = "https://gitlab.example/api/v4/projects/1"

    def __init__(self, trace_error=None, mrs=()):
        self.trace_error = trace_error
        self.mrs = list(mrs)
        self.urls = []

    def iter_lines(self, url):
        raise urllib.error.HTTPError(url, self.trace_error, "", {}, None)

    def get(self, url):
        self.urls.append(url)
        q = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
        after = q["updated_after"][0]
        before = q.get("updated_before", ["9999"])[0]
        rows = [m for m in self.mrs if after <= m["updated_at"] <= before]
        return Response(200, {}, json.dumps(rows).encode())


def mr(iid, updated):
    return {"iid": iid, "created_at": updated, "updated_at": updated}


class DeepLogTest(unittest.TestCase):
    def test_only_an_erased_log_is_recorded(self):
        with tempfile.TemporaryDirectory() as d:
            fetch._fetch_deep(FakeGitLab(trace_error=403), d, 7)
            self.assertFalse(os.path.exists(os.path.join(d, "7.gz")))
            fetch._fetch_deep(FakeGitLab(trace_error=404), d, 7)
            self.assertTrue(os.path.exists(os.path.join(d, "7.gz")))

    def test_timestamped_fail_line_is_kept(self):
        line = (
            b"2026-10-17T01:14:20.697475Z 01O FAIL: blockscanner_rewind_test.go:454:"
            b" BlockScannerExtraSuite.TestRollbackChannelClearsStalePrefetch"
        )
        self.assertTrue(fetch.DEEP_KEEP.search(line))
        self.assertFalse(fetch.DEEP_KEEP.search(b"2026-10-17T01:14:20Z 01O ok FAILED"))


class JobsResumeTest(unittest.TestCase):
    def test_interrupted_walk_is_redone_from_the_last_finished_one(self):
        def job(i, day):
            return {"id": i, "status": "success", "name": "j", "created_at": day}

        class Jobs:
            api = FakeGitLab.api

            def __init__(self, rows, fail_after=None):
                self.rows, self.fail_after, self.pages = rows, fail_after, 0

            def get(self, url):
                if self.pages == self.fail_after:
                    raise KeyboardInterrupt
                self.pages += 1
                q = urllib.parse.parse_qs(urllib.parse.urlparse(url).query)
                start = int(q.get("page", ["0"])[0])
                page = self.rows[start:][:1]
                link = f'<{self.api}/jobs?page={start + 1}>; rel="next"'
                return Response(200, {"Link": link}, json.dumps(page).encode())

        old = [job(1, "2026-01-01T00:00:00Z")]
        new = [job(i, f"2026-02-{i:02d}T00:00:00Z") for i in range(20, 1, -1)]
        # the store reaches --since, so no backfill
        since = old[0]["created_at"]
        with tempfile.TemporaryDirectory() as d:
            fetch.fetch_jobs(Jobs(old), d, since, 3)
            with self.assertRaises(KeyboardInterrupt):
                fetch.fetch_jobs(Jobs(new + old, fail_after=2), d, since, 3)
            fetch.fetch_jobs(Jobs(new + old), d, since, 3)
            self.assertEqual(set(fetch.load_jobs(d)), {1} | {j["id"] for j in new})


class MRBackfillTest(unittest.TestCase):
    def test_earlier_since_backfills_old_mrs(self):
        gl = FakeGitLab(
            mrs=[mr(1, "2024-05-01T00:00:00Z"), mr(2, "2025-06-01T00:00:00Z")]
        )
        with tempfile.TemporaryDirectory() as d:
            fetch.fetch_mrs(gl, d, "2025-01-01T00:00:00Z")
            stored = {m["iid"] for m in fetch.iter_jsonl(os.path.join(d, "mrs.jsonl"))}
            self.assertEqual(stored, {2})
            fetch.fetch_mrs(gl, d, "2024-06-01T00:00:00Z")
            stored = {m["iid"] for m in fetch.iter_jsonl(os.path.join(d, "mrs.jsonl"))}
            self.assertEqual(stored, {1, 2})
            self.assertTrue("updated_before=" in gl.urls[-1])
            # the window is covered now: only the incremental read
            n = len(gl.urls)
            fetch.fetch_mrs(gl, d, "2024-06-01T00:00:00Z")
            self.assertEqual(len(gl.urls), n + 1)
            self.assertFalse("updated_before=" in gl.urls[-1])


if __name__ == "__main__":
    unittest.main()
