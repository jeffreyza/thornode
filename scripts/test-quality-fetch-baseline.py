#!/usr/bin/env python3
"""Download the latest successful target-branch unit coverage when accessible."""

import os
import sys
from pathlib import Path
from urllib.parse import quote
from urllib.request import Request, urlopen


def main():
    destination = Path(sys.argv[1])
    variables = os.environ
    keys = (
        "CI_API_V4_URL",
        "CI_PROJECT_ID",
        "CI_MERGE_REQUEST_TARGET_BRANCH_NAME",
        "CI_JOB_TOKEN",
    )
    if any(not variables.get(key) for key in keys):
        print("Baseline unavailable: missing GitLab MR or job-token metadata")
        return 0
    base = variables["CI_API_V4_URL"].rstrip("/")
    project = quote(variables["CI_PROJECT_ID"], safe="")
    ref = quote(variables["CI_MERGE_REQUEST_TARGET_BRANCH_NAME"], safe="")
    url = f"{base}/projects/{project}/jobs/artifacts/{ref}/raw/coverage.txt?job=unit-tests"
    request = Request(url, headers={"JOB-TOKEN": variables["CI_JOB_TOKEN"]})
    try:
        with urlopen(request, timeout=10) as response:
            contents = response.read()
        if not contents.startswith(b"mode: "):
            raise ValueError("response was not a Go coverage profile")
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes(contents)
        print(
            f"Baseline coverage: {len(contents)} bytes from {variables['CI_MERGE_REQUEST_TARGET_BRANCH_NAME']}"
        )
    except Exception as exc:
        print(f"Baseline unavailable: {type(exc).__name__}: {exc}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
