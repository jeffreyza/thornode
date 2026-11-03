#!/usr/bin/env python3
"""Run one Go build configuration and make its test selection auditable."""

import argparse
import json
import os
import re
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path


def events_to_report(lines, required):
    tests = {}
    packages = set()
    output = []
    for line in lines:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            output.append(line.rstrip())
            continue
        package = event.get("Package", "")
        if package:
            packages.add(package)
        action = event.get("Action")
        name = event.get("Test")
        if name and action in {"pass", "fail", "skip"}:
            tests[(package, name)] = action
        if action == "output":
            output.append(event.get("Output", "").rstrip())
    # Nested subtests are useful in JUnit, but counting only top-level tests
    # makes the minimum independent of how many cases a table happens to have.
    executed = sum(v in ("pass", "fail") for (_, n), v in tests.items() if "/" not in n)
    skipped = sum(v == "skip" for (_, n), v in tests.items() if "/" not in n)
    missing = [
        name
        for name in required
        if not any(n == name and v == "pass" for (_, n), v in tests.items())
    ]
    return tests, packages, output, executed, skipped, missing


def gocheck_counts(lines):
    counts = {}
    for line in lines:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if event.get("Action") != "output" or not event.get("Package"):
            continue
        match = re.search(r"OK: (\d+) passed", event.get("Output", ""))
        if match:
            package = event["Package"]
            counts[package] = max(counts.get(package, 0), int(match.group(1)))
    return counts


def write_junit(path, tests, output, command_failed, selection_errors=()):
    setup_failure = int(command_failed and not any(v == "fail" for v in tests.values()))
    suite = ET.Element(
        "testsuite",
        name=path.stem,
        tests=str(len(tests) + setup_failure + len(selection_errors)),
        failures=str(
            sum(v == "fail" for v in tests.values())
            + setup_failure
            + len(selection_errors)
        ),
        skipped=str(sum(v == "skip" for v in tests.values())),
    )
    for (package, name), state in sorted(tests.items()):
        case = ET.SubElement(suite, "testcase", classname=package, name=name)
        if state == "skip":
            ET.SubElement(case, "skipped")
        elif state == "fail":
            ET.SubElement(case, "failure", message="go test failed").text = "\n".join(
                output[-100:]
            )
    if command_failed and not any(v == "fail" for v in tests.values()):
        case = ET.SubElement(
            suite, "testcase", classname="go test", name="package setup"
        )
        ET.SubElement(
            case, "failure", message="go test failed before a test completed"
        ).text = "\n".join(output[-100:])
    for error in selection_errors:
        case = ET.SubElement(suite, "testcase", classname="test selection", name=error)
        ET.SubElement(case, "failure", message=error)
    ET.ElementTree(suite).write(path, encoding="utf-8", xml_declaration=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--module", default=".")
    parser.add_argument("--tags", default="")
    parser.add_argument("--race", action="store_true")
    parser.add_argument(
        "--fresh", action="store_true", help="disable Go test result caching"
    )
    parser.add_argument("--run", default="")
    parser.add_argument("--timeout", default="20m")
    parser.add_argument("--parallel", type=int, default=1)
    parser.add_argument("--min-tests", type=int, default=1)
    parser.add_argument("--max-skips", type=int)
    parser.add_argument(
        "--allow-skip-test",
        action="append",
        default=[],
        help="allow this named top-level skip; any other skip fails when set",
    )
    parser.add_argument(
        "--require-gocheck",
        action="append",
        default=[],
        help="package import suffix and minimum gocheck cases, as suffix:count",
    )
    parser.add_argument("--require-test", action="append", default=[])
    parser.add_argument(
        "--require-package-tests",
        action="append",
        default=[],
        help="package import path suffix that must execute a top-level test",
    )
    parser.add_argument(
        "--forbid-skip-package",
        action="append",
        default=[],
        help="package import path suffix where top-level skips are forbidden",
    )
    parser.add_argument(
        "--exclude-prefix",
        action="append",
        default=[],
        help="explicit import path prefix to exclude after go list",
    )
    parser.add_argument(
        "--test-arg",
        action="append",
        default=[],
        help="additional go test binary argument, placed after packages",
    )
    parser.add_argument("--report-dir", required=True)
    parser.add_argument("packages", nargs="+", help="Go package patterns")
    args = parser.parse_args()
    report = Path(args.report_dir)
    report.mkdir(parents=True, exist_ok=True)
    base = Path(args.module)
    env = os.environ.copy()
    env.setdefault("GOMAXPROCS", "4")
    list_cmd = [
        "go",
        "list",
        "-tags",
        args.tags,
        "-f",
        "{{.ImportPath}}",
        *args.packages,
    ]
    listed = subprocess.run(list_cmd, cwd=base, env=env, text=True, capture_output=True)
    if listed.returncode:
        print(listed.stderr, file=sys.stderr)
        return listed.returncode
    all_packages = sorted(set(listed.stdout.splitlines()))
    packages = [
        package
        for package in all_packages
        if not any(
            package == prefix or package.startswith(prefix + "/")
            for prefix in args.exclude_prefix
        )
    ]
    if not packages:
        print("go list selected zero packages", file=sys.stderr)
        return 1
    (report / "inventory.json").write_text(
        json.dumps(
            {
                "module": str(base),
                "tags": args.tags,
                "packages": packages,
                "excluded_packages": sorted(set(all_packages) - set(packages)),
                "patterns": args.packages,
            },
            indent=2,
        )
        + "\n"
    )
    command = [
        "go",
        "test",
        "-json",
        "-p",
        "2",
        "-parallel",
        str(args.parallel),
        "-timeout",
        args.timeout,
        "-tags",
        args.tags,
        "-coverprofile",
        str(report.resolve() / "coverage.raw"),
    ]
    if args.fresh:
        command.append("-count=1")
    if args.race:
        command += ["-race", "-covermode=atomic"]
    if args.run:
        command += ["-run", args.run]
    command += packages
    command += args.test_arg
    print("$", " ".join(command), flush=True)
    with (report / "events.jsonl").open("w") as stream:
        proc = subprocess.Popen(
            command, cwd=base, env=env, text=True, stdout=stream, stderr=subprocess.PIPE
        )
        _, stderr = proc.communicate()
    lines = (report / "events.jsonl").read_text().splitlines()
    tests, observed, output, executed, skipped, missing = events_to_report(
        lines, args.require_test
    )
    checks = gocheck_counts(lines)
    missing_packages = [
        suffix
        for suffix in args.require_package_tests
        if not any(
            package.endswith(suffix) and "/" not in name and state in ("pass", "fail")
            for (package, name), state in tests.items()
        )
    ]
    forbidden_skips = sorted(
        f"{package}:{name}"
        for (package, name), state in tests.items()
        if state == "skip"
        and "/" not in name
        and any(package.endswith(suffix) for suffix in args.forbid_skip_package)
    )
    if args.allow_skip_test:
        # A skip outside the allowlist fails the run just as a forbidden package
        # skip does, so it is reported in the same summary field.
        forbidden_skips = sorted(
            set(forbidden_skips)
            | {
                f"{package}:{name}"
                for (package, name), state in tests.items()
                if state == "skip"
                and "/" not in name
                and name not in args.allow_skip_test
            }
        )
    selection_errors = [f"required test did not pass: {name}" for name in missing]
    selection_errors += [
        f"package executed no tests: {name}" for name in missing_packages
    ]
    selection_errors += [f"unexpected skipped test: {name}" for name in forbidden_skips]
    for requirement in args.require_gocheck:
        try:
            suffix, minimum = requirement.rsplit(":", 1)
            minimum = int(minimum)
        except ValueError:
            parser.error(f"invalid --require-gocheck value: {requirement}")
        actual = max(
            (count for package, count in checks.items() if package.endswith(suffix)),
            default=0,
        )
        if actual < minimum:
            selection_errors.append(
                f"gocheck cases for {suffix}: {actual}, minimum {minimum}"
            )
    if executed < args.min_tests:
        selection_errors.append(f"executed {executed} tests, minimum {args.min_tests}")
    if args.max_skips is not None and skipped > args.max_skips:
        selection_errors.append(f"skipped {skipped} tests, maximum {args.max_skips}")
    write_junit(
        report / "junit.xml", tests, output, proc.returncode != 0, selection_errors
    )
    if stderr:
        print(stderr, file=sys.stderr)
    if proc.returncode or selection_errors:
        print("\n".join(output[-100:]), file=sys.stderr)
    summary = {
        "command": command,
        "listed_packages": packages,
        "observed_packages": sorted(observed),
        "executed_top_level": executed,
        "skipped_top_level": skipped,
        "required_tests_not_passed": missing,
        "required_packages_without_executed_tests": missing_packages,
        "unexpected_skips": forbidden_skips,
        "gocheck_passes": checks,
        "go_test_exit": proc.returncode,
        "selection_errors": selection_errors,
    }
    (report / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary, indent=2))
    if (report / "coverage.raw").exists():
        # Keep generated protobuf code out of coverage, as the root target does.
        with (report / "coverage.raw").open() as source, (report / "coverage.txt").open(
            "w"
        ) as dest:
            for line in source:
                if ".pb.go:" not in line:
                    dest.write(line)
    return int(proc.returncode != 0 or bool(selection_errors) or not observed)


if __name__ == "__main__":
    sys.exit(main())
