#!/usr/bin/env python3
"""Inventory Go modules, build constraints, and test files in the checkout.

With --check-selection, also fail when a *_test.go file is not compiled by any
CI test configuration below, e.g. a new nested module or a build tag that no
job builds.
"""

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

EXCLUDED = {
    ".git",
    ".cache",
    "vendor",
    "node_modules",
    "test-quality-reports",
    "target",
}


# Each entry mirrors the `go test` selection of a GitLab job:
# (module directory, build tags, package patterns, job, gocheck -check.f filter
# or None). Update it with the job.
SELECTIONS = (
    (".", "mocknet", ("./...",), "unit-tests (go-tss: path-filtered go-tss job)", None),
    (".", "stagenet", ("./common",), "unit-tests test-network-specific", None),
    (".", "chainnet", ("./common",), "unit-tests test-network-specific", None),
    (
        ".",
        "mainnet",
        ("./common", "./bifrost/pkg/chainclients/utxo/..."),
        "unit-tests test-network-specific",
        None,
    ),
    (
        ".",
        "mainnet",
        ("./x/thorchain",),
        "unit-tests test-network-specific",
        "Migrate|Migration19to20Suite|EthThorRestoreSuite",
    ),
    (
        ".",
        "mainnet",
        ("./bifrost/pkg/chainclients/monero",),
        "test-mainnet-monero",
        None,
    ),
    ("test/simulation", "mocknet", ("./...",), "test-simulation-unit", None),
)

# Test files no CI job compiles today. Each entry needs a reason; an entry that
# becomes selected or is deleted fails the check so the list cannot go stale.
KNOWN_UNSELECTED = {
    "bifrost/pkg/chainclients/utxo/bitcoin_signer_testnet_test.go": "testnet tag; no job builds testnet",
    "bifrost/pkg/chainclients/utxo/bitcoin_testnet_test.go": "testnet tag; no job builds testnet",
    "bifrost/tss/go-tss/tss/frost_keygen_faultinject_mainnet_test.go": "!mocknet; go-tss job builds mocknet only",
}


def disk_test_files(root):
    files = set()
    for path in root.rglob("*_test.go"):
        parts = path.relative_to(root).parts
        # The go tool ignores testdata and directories starting with . or _.
        if any(
            part in EXCLUDED or part == "testdata" or part[:1] in "._"
            for part in parts[:-1]
        ):
            continue
        files.add(str(path.relative_to(root)))
    return files


def selected_test_files(root, selections=SELECTIONS):
    """Map each compiled test file to its job labels and to the -check.f
    filters it runs under; None in the filter list means no filter."""
    selected = {}
    filters = {}
    template = (
        "{{$d := .Dir}}{{range .TestGoFiles}}{{$d}}/{{.}}\n{{end}}"
        "{{range .XTestGoFiles}}{{$d}}/{{.}}\n{{end}}"
    )
    resolved_root = root.resolve()
    for module, tags, patterns, job, check_filter in selections:
        result = subprocess.run(
            ["go", "list", "-tags", tags, "-f", template, *patterns],
            cwd=root / module,
            text=True,
            capture_output=True,
        )
        if result.returncode:
            raise RuntimeError(
                f"go list -tags {tags} {' '.join(patterns)} in {module} failed:\n{result.stderr}"
            )
        for line in result.stdout.splitlines():
            if line.strip():
                relative = str(Path(line).resolve().relative_to(resolved_root))
                label = f"{job} [{tags}]"
                if check_filter:
                    label += f" -check.f {check_filter}"
                selected.setdefault(relative, []).append(label)
                filters.setdefault(relative, []).append(check_filter)
    return selected, filters


GOCHECK_METHOD = re.compile(
    r"^func \((?:\w+ )?\*?(\w+)\) (Test\w*)\(\w+ \*(?:\w+\.)?C\)", re.MULTILINE
)


def filter_errors(root, filters):
    """Flag gocheck tests that are compiled only under -check.f filters none of
    which match them: the file passes the selection check but never runs."""
    errors = []
    for name, file_filters in sorted(filters.items()):
        if None in file_filters:
            continue
        source = (root / name).read_text(errors="replace")
        for suite, method in GOCHECK_METHOD.findall(source):
            test = f"{suite}.{method}"
            # gocheck applies -check.f as an unanchored regexp search.
            if not any(re.search(f, test) for f in file_filters):
                errors.append(
                    f"gocheck test excluded by every -check.f filter that compiles it: "
                    f"{name}: {test} (filters: {', '.join(file_filters)})"
                )
    return errors


def selection_errors(disk, selected, known=KNOWN_UNSELECTED):
    errors = [
        f"test file not compiled by any CI job: {name}"
        for name in sorted(disk - set(selected) - set(known))
    ]
    errors += [
        f"stale KNOWN_UNSELECTED entry (now selected or deleted): {name}"
        for name in sorted(known)
        if name in selected or name not in disk
    ]
    return errors


def inventory(root):
    modules = []
    constraints = {}
    test_files = {}
    for path in root.rglob("go.mod"):
        if not any(part in EXCLUDED for part in path.parts):
            modules.append(str(path.parent.relative_to(root)))
    for path in root.rglob("*.go"):
        if any(part in EXCLUDED for part in path.parts):
            continue
        relative = str(path.relative_to(root))
        with path.open(errors="replace") as source:
            first_lines = [source.readline() for _ in range(12)]
        for line in first_lines:
            if line.startswith("//go:build "):
                constraints[relative] = line.removeprefix("//go:build ").strip()
                break
        if path.name.endswith("_test.go"):
            package = str(path.parent.relative_to(root))
            test_files[package] = test_files.get(package, 0) + 1
    return {
        "modules": sorted(modules),
        "build_constraints": dict(sorted(constraints.items())),
        "test_file_counts_by_directory": dict(sorted(test_files.items())),
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True)
    parser.add_argument(
        "--check-selection",
        action="store_true",
        help="fail if a test file is not compiled by any CI test configuration",
    )
    args = parser.parse_args()
    root = Path(".")
    result = inventory(root)
    errors = []
    if args.check_selection:
        selected, filters = selected_test_files(root)
        errors = selection_errors(disk_test_files(root), selected)
        errors += filter_errors(root, filters)
        result["selected_test_files"] = dict(sorted(selected.items()))
        result["known_unselected_test_files"] = KNOWN_UNSELECTED
        result["selection_errors"] = errors
    destination = Path(args.output)
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(result, indent=2) + "\n")
    print(
        f"Inventory: {len(result['modules'])} modules, {len(result['build_constraints'])} tagged files, "
        f"{len(result['test_file_counts_by_directory'])} test directories"
    )
    for error in errors:
        print(error, file=sys.stderr)
    sys.exit(1 if errors else 0)
