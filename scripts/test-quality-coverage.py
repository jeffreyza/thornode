#!/usr/bin/env python3
"""Report coverage of changed Go lines and compare profiles when a base is available."""

import argparse
import json
import re
import subprocess
from collections import defaultdict
from pathlib import Path


def profile(path):
    blocks = defaultdict(list)
    for line in Path(path).read_text().splitlines()[1:]:
        match = re.match(r"^(.+):(\d+)\.\d+,(\d+)\.\d+ (\d+) (\d+)$", line)
        if match and ".pb.go" not in match[1]:
            filename, start, end, statements, hits = match.groups()
            blocks[filename].append((int(start), int(end), int(statements), int(hits)))
    return blocks


def changed_lines(base):
    result = subprocess.run(
        ["git", "diff", "--unified=0", "--no-ext-diff", base, "HEAD", "--", "*.go"],
        text=True,
        capture_output=True,
        check=True,
    )
    changed = defaultdict(set)
    filename = None
    for line in result.stdout.splitlines():
        if line.startswith("+++ b/"):
            filename = line[6:]
        elif line.startswith("@@") and filename:
            match = re.search(r"\+(\d+)(?:,(\d+))?", line)
            if match:
                start, length = int(match[1]), int(match[2] or 1)
                changed[filename].update(range(start, start + length))
    return changed


def summarize(blocks, changed):
    result = defaultdict(lambda: [0, 0])
    for filename, entries in blocks.items():
        repo_path = filename.split("/v3/", 1)[-1] if "/v3/" in filename else filename
        if repo_path not in changed:
            continue
        for start, end, statements, hits in entries:
            if any(start <= number <= end for number in changed[repo_path]):
                package = str(Path(repo_path).parent)
                result[package][0] += statements
                result[package][1] += statements * int(hits > 0)
    return dict(sorted(result.items()))


def package_summary(blocks, packages):
    result = defaultdict(lambda: [0, 0])
    for filename, entries in blocks.items():
        repo_path = filename.split("/v3/", 1)[-1] if "/v3/" in filename else filename
        package = str(Path(repo_path).parent)
        if package not in packages:
            continue
        for _, _, statements, hits in entries:
            result[package][0] += statements
            result[package][1] += statements * int(hits > 0)
    return dict(sorted(result.items()))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True)
    parser.add_argument("--base-ref", required=True)
    parser.add_argument("--baseline-profile")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    changed = changed_lines(args.base_ref)
    current_profile = profile(args.profile)
    current = summarize(current_profile, changed)
    changed_packages = {str(Path(name).parent) for name in changed}
    current_packages = package_summary(current_profile, changed_packages)
    baseline = (
        package_summary(profile(args.baseline_profile), changed_packages)
        if args.baseline_profile
        else None
    )
    report = {
        "base_ref": args.base_ref,
        "changed_go_files": sorted(changed),
        "changed_lines_by_package": current,
        "changed_package_coverage": current_packages,
        "baseline_available": baseline is not None,
    }
    if baseline is not None:
        report["baseline_package_coverage"] = baseline
        report["regressions"] = {
            name: {"base": prior, "current": current_packages[name]}
            for name, prior in baseline.items()
            if prior[0]
            and current_packages.get(name, [0, 0])[0]
            and current_packages[name][1] * prior[0]
            < prior[1] * current_packages[name][0]
        }
    Path(args.output).parent.mkdir(parents=True, exist_ok=True)
    Path(args.output).write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
