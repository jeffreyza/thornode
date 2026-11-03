#!/usr/bin/env python3
"""Discover Loki job labels and cache them in /tmp/tc_loki_jobs.json.

Usage: python3 discover_loki.py [logcli_addr]

Discovers all job labels from Loki, filters for thornode/bifrost-related jobs,
and caches results in /tmp/tc_loki_jobs.json with a TTL of 1 hour.

Output JSON structure:
{
  "timestamp": "2024-01-01T00:00:00Z",
  "addr": "http://localhost:3100",
  "thornode_jobs": ["job1", ...],
  "bifrost_jobs": ["job2", ...],
  "all_jobs": ["job1", "job2", ...]
}
"""

import json
import os
import subprocess
import sys
from datetime import datetime, timezone

CACHE_FILE = "/tmp/tc_loki_jobs.json"
CACHE_TTL_SECONDS = 3600  # 1 hour


def load_cache():
    """Load cached jobs if fresh enough."""
    if not os.path.exists(CACHE_FILE):
        return None
    try:
        with open(CACHE_FILE) as f:
            data = json.load(f)
        ts = datetime.fromisoformat(data["timestamp"])
        age = (datetime.now(timezone.utc) - ts).total_seconds()
        if age < CACHE_TTL_SECONDS:
            return data
        print(
            f"Cache expired ({int(age)}s old, TTL={CACHE_TTL_SECONDS}s)",
            file=sys.stderr,
        )
    except (json.JSONDecodeError, KeyError, ValueError) as e:
        print(f"Cache invalid: {e}", file=sys.stderr)
    return None


def discover_jobs(addr):
    """Query Loki for all job labels."""
    try:
        result = subprocess.run(
            ["logcli", "--addr", addr, "labels", "job", "--since=24h"],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            print(f"logcli error: {result.stderr.strip()}", file=sys.stderr)
            return []
        lines = [
            line.strip() for line in result.stdout.strip().split("\n") if line.strip()
        ]
        return lines
    except FileNotFoundError:
        print("logcli not found in PATH", file=sys.stderr)
        return []
    except subprocess.TimeoutExpired:
        print("logcli timed out", file=sys.stderr)
        return []


def classify_jobs(jobs):
    """Classify jobs into thornode and bifrost categories."""
    thornode_jobs = []
    bifrost_jobs = []
    for job in jobs:
        jl = job.lower()
        if "thornode" in jl or "thor-node" in jl or "thor_node" in jl:
            thornode_jobs.append(job)
        elif "bifrost" in jl:
            bifrost_jobs.append(job)
    return thornode_jobs, bifrost_jobs


def main():
    addr = sys.argv[1] if len(sys.argv) > 1 else "http://localhost:3100"

    # Check cache first
    cached = load_cache()
    if cached and cached.get("addr") == addr:
        print(json.dumps(cached, indent=2))
        return

    all_jobs = discover_jobs(addr)
    if not all_jobs:
        print(json.dumps({"error": "No jobs discovered", "addr": addr}))
        sys.exit(1)

    thornode_jobs, bifrost_jobs = classify_jobs(all_jobs)

    data = {
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "addr": addr,
        "thornode_jobs": thornode_jobs,
        "bifrost_jobs": bifrost_jobs,
        "all_jobs": all_jobs,
    }

    with open(CACHE_FILE, "w") as f:
        json.dump(data, f, indent=2)

    print(
        f"Discovered {len(thornode_jobs)} thornode job(s), {len(bifrost_jobs)} bifrost job(s) "
        f"out of {len(all_jobs)} total",
        file=sys.stderr,
    )
    print(json.dumps(data, indent=2))


if __name__ == "__main__":
    main()
