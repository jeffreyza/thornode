"""Interdiff stage: for each resolved episode, what did the branch itself change
between the last failing version and the first passing one?

The comparison is rebase-insensitive: each version's own patch is its diff from
its merge base with develop, and only files whose own patch differs count. A
rebase onto a newer develop therefore changes nothing by itself. After a rebase
the patches are taken without context lines, which would otherwise pick up
develop's own edits next to the MR's.

Once an MR has merged, develop contains its heads, so both patches are taken
against the develop commit just before that merge instead; a fast-forward merge
leaves no such commit and the pair is reported as merged (fix type unknown).

Results are cached in interdiff.jsonl, keyed by (red head, green head).
Commits missing from the git mirror are fetched by SHA (GitLab keeps every
pipeline commit reachable), unless --no-git-fetch.
"""

import base64
import json
import os
import re
import subprocess
import threading
from concurrent.futures import ThreadPoolExecutor

from gitlab import log

GEN = re.compile(
    r"(\.pb\.go|\.pb\.gw\.go|\.pulsar\.go|_mock\.go|mocks?/|openapi/gen/|docs/.*swagger|/gen/|\.generated\.)"
)
GOLDEN = re.compile(
    r"^test/regression/mnt/exports/|^test/regression/mnt/blocks/|\.golden$|/golden/|/testdata/"
)
TEST = re.compile(
    r"(_test\.go$|^test/|/test/|_test\.py$|^scripts/tests/|/tests/|testutil|/fixtures?/|\.snap$)"
)
CI = re.compile(
    r"^(\.gitlab-ci\.yml|\.gitlab/|ci/|Makefile|build/|scripts/|\.trunk/|\.golangci|\.semgrep|\.govulncheck|Dockerfile|docker-compose|go\.mod$|go\.sum$|\.codespell|\.markdownlint|tools/)"
)
DOCS = re.compile(r"(\.md$|^docs/|CHANGELOG|\.txt$|\.rst$)")


CACHE_VERSION = 3


class Mirror:
    def __init__(self, path, develop_ref, remote_url=None, token=None):
        self.path = path
        self.develop_ref = develop_ref
        self.remote_url = remote_url
        self._fp = None
        self._lock = threading.Lock()
        self.env = dict(os.environ, GIT_TERMINAL_PROMPT="0")
        if token:
            # passed in the environment, not argv, so it stays out of `ps`
            auth = base64.b64encode(f"oauth2:{token}".encode()).decode()
            self.env.update(
                GIT_CONFIG_COUNT="1",
                GIT_CONFIG_KEY_0="http.extraHeader",
                GIT_CONFIG_VALUE_0=f"Authorization: Basic {auth}",
            )

    def git(self, *a, check=True, inp=None):
        r = subprocess.run(
            ["git", "-C", self.path] + list(a),
            capture_output=True,
            text=True,
            errors="replace",
            input=inp,
            env=self.env,
        )
        if check and r.returncode:
            raise RuntimeError(r.stderr[:300])
        return r.stdout

    def have(self, sha):
        r = subprocess.run(
            ["git", "-C", self.path, "cat-file", "-e", sha + "^{commit}"],
            capture_output=True,
        )
        return r.returncode == 0

    def is_ancestor(self, a, b):
        r = subprocess.run(
            ["git", "-C", self.path, "merge-base", "--is-ancestor", a, b],
            capture_output=True,
        )
        return r.returncode == 0

    def first_parent(self):
        """{sha: distance from the tip} along develop's first-parent line."""
        with self._lock:
            if self._fp is None:
                out = self.git("rev-list", "--first-parent", self.develop_ref)
                self._fp = {c: i for i, c in enumerate(out.split())}
        return self._fp

    def missing(self, shas):
        if not shas:
            return []
        out = self.git("cat-file", "--batch-check", inp="\n".join(shas) + "\n")
        return [ln.split()[0] for ln in out.splitlines() if ln.endswith(" missing")]

    def sync(self, shas):
        """Create the mirror if needed, update develop, fetch missing commits."""
        if not os.path.isdir(self.path):
            subprocess.run(["git", "init", "--quiet", "--bare", self.path], check=True)
            self.git("remote", "add", "origin", self.remote_url)
        branch = self.develop_ref.rsplit("/", 1)[-1]
        log("git: updating", self.develop_ref)
        self.git(
            "fetch", "--quiet", "--no-tags", "origin", f"+{branch}:{self.develop_ref}"
        )
        self._fp = None
        todo = self.missing(sorted(shas))
        log("git: fetching", len(todo), "missing commits")
        while todo:
            batch, todo = todo[:100], todo[100:]
            self.git("fetch", "--quiet", "--no-tags", "origin", *batch, check=False)
            # one unfetchable SHA fails the whole batch; retry the rest one by one
            for sha in self.missing(batch):
                self.git("fetch", "--quiet", "--no-tags", "origin", sha, check=False)
        left = self.missing(sorted(shas))
        if left:
            log("git:", len(left), "commits could not be fetched")


def split_patch(p):
    files = {}
    cur = None
    buf = []
    for line in p.splitlines():
        if line.startswith("diff --git "):
            if cur:
                files[cur] = "\n".join(buf)
            m = re.match(r"diff --git a/(\S+) b/(\S+)", line)
            cur = m.group(2) if m else line
            buf = []
        elif line.startswith(("@@", "index ")):
            continue
        else:
            buf.append(line)
    if cur:
        files[cur] = "\n".join(buf)
    return files


def merged_into(mirror, head):
    """The first-parent develop commit that merged head; head itself when it
    is on develop's first-parent line (fast-forward); None when unmerged."""
    if not mirror.is_ancestor(head, mirror.develop_ref):
        return None
    fp = mirror.first_parent()
    if head in fp:
        return head
    desc = mirror.git(
        "rev-list", "--ancestry-path", f"{head}..{mirror.develop_ref}"
    ).split()
    # the oldest first-parent descendant is the merge that brought head in
    return max((c for c in desc if c in fp), key=fp.get)


def pair_base(mirror, a, b):
    """What both own patches are measured against.

    Normally develop. Once the MR has merged, develop contains its heads and
    each merge base would be the head itself, so measure against the develop
    commit just before the earlier merge of either head. None after a
    fast-forward merge: no develop commit before the head is free of it.
    """
    merges = [m for m in (merged_into(mirror, a), merged_into(mirror, b)) if m]
    if not merges:
        return mirror.develop_ref
    if a in merges or b in merges:
        return None
    fp = mirror.first_parent()
    return max(merges, key=fp.get) + "^1"


def own_patch(mirror, mb, head, context):
    diff = mirror.git(
        "diff", "--no-color", "--no-ext-diff", "-M", f"-U{context}", mb, head
    )
    return split_patch(diff)


def classify_files(files):
    cls = set()
    for f in files:
        if GOLDEN.search(f):
            cls.add("golden")
        elif GEN.search(f):
            cls.add("generated")
        elif TEST.search(f):
            cls.add("test")
        elif DOCS.search(f):
            cls.add("docs")
        elif CI.search(f):
            cls.add("ci/build")
        else:
            cls.add("code")
    return cls


def analyse(mirror, pair):
    a, b = pair
    if not os.path.isdir(mirror.path) or not (mirror.have(a) and mirror.have(b)):
        return pair, {"status": "missing"}
    try:
        base = pair_base(mirror, a, b)
        if base is None:
            return pair, {"status": "merged"}
        mba = mirror.git("merge-base", a, base, check=False).strip()
        mbb = mirror.git("merge-base", b, base, check=False).strip()
        if not mba or not mbb:
            return pair, {"status": "nobase"}
        # without a rebase, context keeps a fix that only moves a line; after
        # one, it would carry develop's edits near the MR's own
        context = 3 if mba == mbb else 0
        pa = own_patch(mirror, mba, a, context)
        pb = own_patch(mirror, mbb, b, context)
        changed = sorted(f for f in set(pa) | set(pb) if pa.get(f) != pb.get(f))
        tree_a = mirror.git("rev-parse", a + "^{tree}").strip()
        tree_b = mirror.git("rev-parse", b + "^{tree}").strip()
        return pair, {
            "status": "ok",
            "files": changed,
            "classes": sorted(classify_files(changed)),
            "rebased": mba != mbb,
            "tree_same": tree_a == tree_b,
        }
    except RuntimeError as e:
        return pair, {"status": "error", "err": str(e)[:200]}


def episode_pairs(eps):
    return {
        (e["heads"][-1], e["fixed_by"])
        for e in eps
        if e["fixed_by"] and e["ctx"][0] in ("mr", "branch")
    }


def run(data_dir, eps, mirror, fetch, workers):
    """{(red, green): result} for every resolved MR/branch episode."""
    path = os.path.join(data_dir, "interdiff.jsonl")
    cache = {}
    if os.path.exists(path):
        with open(path) as f:
            for line in f:
                r = json.loads(line)
                # older rows measured merged MRs against the current develop,
                # or kept context lines across a rebase
                if r.pop("v", 1) == CACHE_VERSION:
                    cache[tuple(r.pop("pair"))] = r
    pairs = episode_pairs(eps)
    todo = sorted(pairs - cache.keys())
    log("interdiff: pairs", len(pairs), "cached", len(pairs) - len(todo))
    if todo and fetch:
        mirror.sync({sha for pair in todo for sha in pair})
    res = {}
    with open(path, "a") as out, ThreadPoolExecutor(workers) as ex:
        for i, (pair, r) in enumerate(ex.map(lambda p: analyse(mirror, p), todo)):
            res[pair] = r
            # anything else may work after a fetch
            if r["status"] in ("ok", "merged"):
                row = {"pair": list(pair), "v": CACHE_VERSION} | r
                out.write(json.dumps(row) + "\n")
            if i % 500 == 0:
                log("interdiff", i)
    bad = [p for p, r in res.items() if r["status"] != "ok"]
    if bad:
        log("interdiff:", len(bad), "pairs without a result (see README caveats)")
    return {p: res.get(p) or cache[p] for p in pairs}
