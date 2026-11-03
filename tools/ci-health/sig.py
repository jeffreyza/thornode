"""Failure-signature extraction from a failed job's log.

signature(text) -> (kind, key, env_hits)
  kind: coarse class of what the log shows failing
  key:  normalised string for cross-MR matching ("" when nothing specific)
  env_hits: list of environment/infra patterns that matched
"""

import gzip
import os
import re

ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]|\r")
TS = re.compile(r"^\d{4}-\d\d-\d\dT[\d:.]+Z \d\d[OE][+ ]?", re.M)

ENV = [
    (
        "gitlab-load",
        r"GitLab is currently unable to handle this request|fetch-pack: unexpected disconnect|The requested URL returned error: 5\d\d\b|remote: GitLab: .*(timeout|unavailable)|RPC failed; HTTP \d\d\d\b|expected flush after ref listing",
    ),
    (
        "git-fetch",
        r"fatal: unable to access 'https://gitlab\.com|fatal: early EOF|fatal: could not read Username|Failed to connect to gitlab\.com",
    ),
    (
        "dns-net",
        r"lookup (?:[\w-]+\.)+[a-z]{2,}\.?(?::\d+)? [^\n]{0,80}(i/o timeout|no such host|server misbehaving)|dial tcp [^\n]{0,120}network is unreachable|Temporary failure in name resolution|Could not resolve host|TLS handshake timeout|net/http: request canceled while waiting for connection",
    ),
    (
        "registry",
        r"received unexpected HTTP status: 5\d\d\b|unavailable; pulling [\w-]+ from ghcr|toomanyrequests|pull rate limit|error pulling image|failed to pull image|Error response from daemon: (Get|Head) \"https://|unexpected status from (PUT|POST|GET|HEAD) request to https://|denied: requested access to the resource is denied|Error: ImagePull|manifest unknown|unauthorized: authentication required|failed to authorize: failed to fetch oauth token",
    ),
    (
        "docker-daemon",
        r"Cannot connect to the Docker daemon|error during connect|failed to create task|OCI runtime (create|exec) failed",
    ),
    ("disk", r"no space left on device"),
    (
        "oom",
        r"Error 137\b|terminated by signal (9|11)\b|signal: segmentation fault|signal: killed|\bOOMKilled\b|fatal error: runtime: out of memory|exit code 137\b|cannot allocate memory",
    ),
    (
        "goproxy",
        r"proxy\.golang\.org[^\n]{0,200}(stream error|i/o timeout|EOF|\b50\d\b|connection reset|TLS)|sum\.golang\.org[^\n]{0,200}(timeout|\b50\d\b)|go: [^\n]{0,200}(verifying module|reading https://)[^\n]{0,200}(\b50\d\b|timeout|EOF)",
    ),
    (
        "apt-apk",
        r"^E: Failed to fetch|^E: Unable to fetch some archives|Hash Sum mismatch|^ERROR: unable to select packages",
    ),
    (
        "buf",
        r"^Failure: 403 Forbidden|buf\.build[^\n]{0,200}(\b403\b|\b429\b|\b50\d\b|unavailable|deadline|Forbidden)|^Failure: (the server hosted at that remote is unavailable|unavailable)",
    ),
    (
        "artifacts",
        r"ERROR: Uploading artifacts[^\n]{0,200}\b(403|50\d)\b|responseStatus=(403|50\d)\b|ERROR: Downloading artifacts from coordinator\.\.\. (error|forbidden|not found)",
    ),
    (
        "external-http",
        r"(ghcr\.io|github\.com|githubusercontent\.com|pypi\.org|registry\.npmjs\.org|crates\.io|static\.rust-lang\.org)[^\n]{0,200}(\b(403|429|50\d)\b|i/o timeout|timed out|connection reset|unexpected EOF)",
    ),
    ("job-timeout", r"ERROR: Job failed: execution took longer than"),
    # the runner stops a job whose output passes GitLab's log size limit; that
    # is runaway output (often a test spewing), not a timeout
    ("log-limit", r"Job's log exceeded limit"),
    (
        "runner",
        r"ERROR: Job failed \(system failure\)|ERROR: Preparation failed|ERROR: Job failed: prepare environment",
    ),
    (
        "tss-token",
        r"no secure protocol found|no credential can read[^\n]*\n(?:[^\n]*\n){0,16}ERROR: Job failed|go: gitlab\.com/thorchain/tss/tss-lib-private[^\n]{0,200}(\b40[34]\b|denied|Authentication failed|could not read|terminal prompts)",
    ),
    ("trunk-internal", r"Trunk Internal Error|An internal error occurred"),
    ("vulndb", r"vuln\.go\.dev[^\n]{0,200}(\b50\d\b|timeout|EOF|unexpected status)"),
]
ENV = [(n, re.compile(p, re.M)) for n, p in ENV]
TSS_TOKEN = dict(ENV)["tss-token"]
# Since May 2026 most simulation logs print the tss-lib-private fetch error
# ("<file>.go:L:C: no secure protocol found for repository") and carry on for
# minutes. It only failed the job when it is among the log's last lines: a
# failed `go mod download` stops the build and the job ends ~20 lines later.
TSS_FATAL_TAIL_LINES = 60


def ended_job(t, rx, tail_lines):
    """True if rx's last match in t is within tail_lines of the end of the log."""
    last = None
    for last in rx.finditer(t):
        pass
    return last is not None and t.count("\n", last.end()) <= tail_lines


GOFILE_ERR = re.compile(r"^(?:\./)?([\w./-]+\.go):\d+(?::\d+)?: (.+)$", re.M)
PKG_HDR = re.compile(r"^# (gitlab\.com/\S+|\S+)$", re.M)
BUILD_FAILED = re.compile(r"\[(build|setup) failed\]")
GOTEST_FAIL = re.compile(r"^\s*--- FAIL: (\S+)|^=== FAIL: \S+ (\S+)", re.M)
GOTESTSUM_DONE = re.compile(r"^DONE \d+ tests.*?(\d+) failures?", re.M)
GOCHECK_FAIL = re.compile(r"^(?:FAIL|PANIC): (\S+?):\d+: (\w+\.\w+)", re.M)
GOPKG_FAIL = re.compile(r"^FAIL\s+(gitlab\.com/\S+)\s", re.M)
RACE = re.compile(r"WARNING: DATA RACE")
PANIC = re.compile(r"^panic: (.{0,120})", re.M)
LINTER = re.compile(
    r"^(?:\./)?([\w./-]+\.\w+):\d+(?::\d+)?:? .*\((\w[\w-]*)\)\s*$", re.M
)
COMPILE_ERR = re.compile(
    r"^(?:-: )?(?:/builds/thorchain/thornode/)?([\w./-]+\.go):\d+:\d+: (.*(?:undefined|unknown field|cannot use|not enough arguments|too many arguments|declared and not used|imported and not used|has no field or method|mismatched types|invalid operation|missing return|redeclared|already declared|syntax error|cannot find package|could not import|no required module).*)$",
    re.M,
)
TRUNK_HDR_FILE = re.compile(r"^([\w./-]+\.\w+):\d+:\d+\s*$", re.M)
LINT_MISC = re.compile(
    r"✖ \d+ new lint issues?|Detected change in versioned function|^Lint failed\.|\d+ issues?:\s*$",
    re.M,
)
TRUNK = re.compile(
    r"^\s*(\d+):(\d+)\s+(high|medium|low|error|warning|failure)\s+.*\s([\w-]+/[\w./-]+)\s*$",
    re.M,
)
TRUNK_FILE = re.compile(r"^([\w./-]+\.\w+):?\s*$", re.M)
CODESPELL = re.compile(r"^(?:\./)?([\w./-]+):\d+: \w+ ==> ", re.M)
VULN = re.compile(
    r"Vulnerability #\d+: (GO-\d{4}-\d+)|^\s*([\w./-]+@v[\w.+-]+) \(fixed in", re.M
)
TESTQ = re.compile(
    r"test file not compiled by any CI job|EXPECTED AT LEAST \d+|required_tests_not_passed\": \[\s*\"|unexpected_skips\": \[\s*\"|coverage[^\n]{0,80}below|selection_errors\": \[\s*\""
)
REG_FAILED = re.compile(r"^Failed: (\d+)\s*$((?:\n- \S+)+)", re.M)
REG_FAIL_LINE = re.compile(r"^(?:FAIL|>> FAIL)\S*[: ]+\s*(suites/\S+\.yaml)", re.M)
REG_CMP = re.compile(r"\.\.\. // \d+ identical|^\s*[-+]\s+\"?\w+\"?: ", re.M)
REG_FIELD = re.compile(r"^\s*([-+])\s+\"?(\w+)\"?: ", re.M)
REG_TIMEOUT = re.compile(r"newBlock|new block timeout|timed out waiting", re.I)
REG_404 = re.compile(r"\b404\b|tx not found")
PROTO_BREAK = re.compile(r"^([\w./-]+\.proto):\d+:\d+:(.+)$", re.M)
PYFAIL = re.compile(r"^(?:FAIL|ERROR): (test\w+) \(", re.M)
SIM_ACTOR = re.compile(
    r"actor=([\w.\-]+)[^\n]{0,80}(timeout|timed out|failed|error)|(?:FAIL|failed)[^\n]{0,40}actor[= :]+([\w.\-]+)",
    re.I,
)
SIM_DAG = re.compile(r"dag\.go:\d+[^\n]{0,200}")
SIM_FTL = re.compile(
    r"FTL [^\n]*?actor execution failed error=(\"[^\"]*\"|\S+) actor=\"([^\"]*)\""
)
THORSCAN_DOWN = re.compile(r"thorscan\.go:\d+ > failed to get current height")


def norm_actor(a):
    return re.sub(r"-0X[0-9A-F]+", "", a)


GENDIFF = re.compile(
    r"(generated files? (are|is) (out of date|stale|not up to date)|please run [`']?make (generate|proto-gen|openapi)|git diff --exit-code|Changes not staged for commit|^diff --git a/(\S+)|working tree is dirty)",
    re.I | re.M,
)
GOFMT = re.compile(
    r"(ERR: Go formatting errors|\d+ unformatted files?|code should be clang-formatted|would reformat|^gofumpt|^goimports)",
    re.M,
)


def clean(raw):
    t = raw.decode("utf8", "replace") if isinstance(raw, bytes) else raw
    t = ANSI.sub("", t)
    t = TS.sub("", t)
    return t


def load(jid, data_dir):
    """Filtered full log (deep/) followed by the log tail (traces/), cleaned."""
    p = os.path.join(data_dir, "traces", f"{jid}.gz")
    q = os.path.join(data_dir, "deep", f"{jid}.gz")
    if not os.path.exists(p) and not os.path.exists(q):
        return None
    out = []
    for f in (q, p):
        if os.path.exists(f):
            try:
                with gzip.open(f) as fh:
                    out.append(clean(fh.read()))
            except (OSError, EOFError):
                pass
    return "\n".join(out)


def norm_msg(m):
    m = re.sub(r"0x[0-9a-fA-F]+|\b\d+(\.\d+)?\b", "N", m)
    return m.strip()[:120]


def signature(t):
    if t is None:
        return ("notrace", "", [])
    env = [n for n, rx in ENV if rx.search(t)]
    if "tss-token" in env and not ended_job(t, TSS_TOKEN, TSS_FATAL_TAIL_LINES):
        env.remove("tss-token")
    # compile errors: a "# pkg" header followed by file.go:L:C lines, or [build failed]
    if BUILD_FAILED.search(t) or PKG_HDR.search(t):
        errs = []
        for m in PKG_HDR.finditer(t):
            start, end = m.end(), m.end() + 4000
            seg = t[start:end]
            for e in GOFILE_ERR.finditer(seg):
                pos = e.start()
                if "\n#" in seg[:pos]:
                    break
                errs.append(f"{e.group(1)}: {norm_msg(e.group(2))}")
        if errs or BUILD_FAILED.search(t):
            return ("compile", "|".join(sorted(set(errs))[:5]), env)
    ce = COMPILE_ERR.findall(t)
    if ce:
        return (
            "compile",
            "|".join(sorted(set(f"{f}: {norm_msg(m)}" for f, m in ce))[:5]),
            env,
        )
    vul = sorted(set(a or b for a, b in VULN.findall(t)))
    if vul:
        return ("vuln", ",".join(vul), env)
    pb = PROTO_BREAK.findall(t)
    if pb and re.search(
        r"Previously present|changed (type|label|name)|was deleted|deleted\.", t
    ):
        return (
            "proto-breaking",
            "|".join(sorted(set(f + ":" + norm_msg(m) for f, m in pb))[:5]),
            env,
        )
    reg = REG_FAILED.findall(t)
    suites = []
    if reg and int(reg[-1][0]) > 0:
        suites = sorted(set(re.findall(r"- (\S+)", reg[-1][1])))
    if not suites:
        suites = sorted(set(REG_FAIL_LINE.findall(t)))
    if suites:
        # what kind of regression failure: a deterministic state/export diff vs a timeout/lookup race
        if REG_CMP.search(t):
            # "diff:<suite>" alone says nothing about what differed; add the
            # export fields the diff adds (+) or removes (-), so only the same
            # diff matches across MRs and known flakes
            fields = sorted(set(s + f for s, f in REG_FIELD.findall(t)))
            key = "diff:" + ",".join(suites[:8])
            if fields:
                key += "#" + ",".join(fields[:8])
            return ("regression", key, env)
        elif REG_TIMEOUT.search(t):
            rtype = "timeout"
        elif REG_404.search(t):
            rtype = "notfound"
        else:
            rtype = "other"
        return ("regression", rtype + ":" + ",".join(suites[:8]), env)
    gc = GOCHECK_FAIL.findall(t)
    tests = sorted(set(n for _, n in gc))
    if not tests:
        tests = sorted(
            set((a or b).split("/")[0] for a, b in GOTEST_FAIL.findall(t))
            - {"Test", "TestPackage", "(unknown)"}
        )
    race = bool(RACE.search(t))
    if tests or race:
        return ("race" if race else "gotest", ",".join(tests[:8]), env)
    tq = TESTQ.findall(t)
    if tq:
        return ("test-quality", norm_msg(tq[0]), env)
    if GOTESTSUM_DONE.search(t):
        return ("gotest", "", env)
    py = sorted(set(PYFAIL.findall(t)))
    if py:
        return ("pytest", ",".join(py[:8]), env)
    lint = sorted(set(f"{f}:{l}" for f, l in LINTER.findall(t)))
    trunk = sorted(set(m[3] for m in TRUNK.findall(t)))
    if trunk:
        trunk = sorted(set(TRUNK_HDR_FILE.findall(t))) + trunk
    cs = sorted(set(CODESPELL.findall(t)))
    if lint or trunk or cs:
        return (
            "lint",
            ",".join((lint + trunk + ["codespell:" + c for c in cs])[:10]),
            env,
        )
    ftl = SIM_FTL.findall(t)
    if ftl:
        err, actor = ftl[0]
        return (
            "sim",
            norm_actor(actor) + ": " + norm_msg(re.sub(r"[0-9a-f]{16,}", "H", err)),
            env,
        )
    if THORSCAN_DOWN.search(t):
        return ("sim-node-down", "thornode unreachable", env)
    if SIM_DAG.search(t) or SIM_ACTOR.search(t):
        actors = sorted(set(a or b for a, _, b in SIM_ACTOR.findall(t)) - {""})
        return ("sim", ",".join([a for a in actors if a != "logs"][:5]), env)
    pkgs = sorted(set(GOPKG_FAIL.findall(t)))
    if pkgs:
        return ("gotest-pkg", ",".join(p.split("/v3/")[-1] for p in pkgs[:6]), env)
    p = PANIC.findall(t)
    if p:
        return ("panic", norm_msg(p[0]), env)
    if GENDIFF.search(t):
        files = sorted(set(m for m in re.findall(r"^diff --git a/(\S+)", t, re.M)))
        return ("gendiff", ",".join(files[:8]), env)
    if GOFMT.search(t):
        files = sorted(
            set(
                re.findall(
                    r"^\./([\w./-]+\.proto):\d+:\d+: error: code should be clang",
                    t,
                    re.M,
                )
            )
        )
        return ("format", ",".join(files[:8]), env)
    if re.search(r"^\s*ISSUES\s*$", t, re.M) or LINT_MISC.search(t):
        files = sorted(set(TRUNK_HDR_FILE.findall(t)))
        return ("lint", ",".join(files[:8]), env)
    return ("unknown", "", env)
