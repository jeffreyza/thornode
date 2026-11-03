"""Minimal GitLab REST client on urllib (standard library only)."""

import http.client
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

RETRY_STATUS = {429, 500, 502, 503, 504}
LINK_NEXT = re.compile(r'<([^>]+)>;\s*rel="next"')


def log(*a):
    print(*a, file=sys.stderr, flush=True)


def get_token(env_name, host, required=True):
    """The API token: $<env_name> if set, else `glab config get token`."""
    tok = os.environ.get(env_name, "").strip()
    if tok:
        return tok
    try:
        tok = subprocess.check_output(
            ["glab", "config", "get", "token", "--host", host],
            text=True,
            stderr=subprocess.DEVNULL,
        ).strip()
    except (OSError, subprocess.CalledProcessError):
        tok = ""
    if not tok and required:
        sys.exit(f"no GitLab token: set ${env_name} or run `glab auth login`")
    return tok


class Response:
    def __init__(self, status, headers, body):
        self.status = status
        self.headers = headers
        self.body = body

    def json(self):
        # job titles and log lines carry raw control characters
        return json.loads(self.body, strict=False)

    def next_url(self):
        m = LINK_NEXT.search(self.headers.get("Link") or "")
        return m.group(1) if m else None


class GitLab:
    def __init__(self, url, project, token):
        self.host = urllib.parse.urlparse(url).hostname
        self.api = f"{url.rstrip('/')}/api/v4/projects/{project}"
        self.token = token

    def _open(self, url, headers, timeout):
        hdrs = {"PRIVATE-TOKEN": self.token}
        hdrs.update(headers or {})
        return urllib.request.urlopen(
            urllib.request.Request(url, headers=hdrs), timeout=timeout
        )

    def get(self, url, headers=None, timeout=120, attempts=8):
        """GET with retries on 429/5xx and network errors.

        Any other HTTP error is returned as a Response, not raised, so callers
        can handle 404 (erased log) and 416 (log shorter than the Range).
        """
        err = None
        for attempt in range(attempts):
            try:
                with self._open(url, headers, timeout) as r:
                    return Response(r.status, r.headers, r.read())
            except urllib.error.HTTPError as e:
                if e.code not in RETRY_STATUS:
                    return Response(e.code, e.headers, e.read())
                err = f"http {e.code}"
            except (urllib.error.URLError, OSError, http.client.HTTPException) as e:
                err = str(e)
            log("retry", attempt, err)
            time.sleep(min(2**attempt, 60))
        raise RuntimeError(f"giving up on {url}: {err}")

    def iter_lines(self, url, timeout=180):
        """Stream a response body as lines split like bytes.splitlines()."""
        with self._open(url, None, timeout) as r:
            for raw in r:
                yield from raw.splitlines()
