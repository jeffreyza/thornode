#!/usr/bin/env bash

# Give every tracked file an mtime derived from its content, and every
# directory a fixed one, so `go test` can reuse cached results across CI jobs.
#
# go test caches a passing package's result keyed on the test binary and on
# the size and mtime of each file and directory the tests opened. A fresh
# clone stamps everything with the clone time, so every package that reads a
# testdata file or fixture misses the cache on every job, even when nothing it
# depends on changed. With the mtime taken from the git blob id, a file keeps
# its mtime until its content changes, and then gets a different one.
#
# The mtimes fall between 1990 and 2024: in the past, because go test does not
# cache a result that read a file modified in the last two seconds.

set -euo pipefail

git ls-files -s -z | perl -0 -ne '
  my ($meta, $path) = split /\t/, $_, 2;
  chop $path if substr($path, -1) eq "\0";
  my ($mode, $blob) = split / /, $meta;
  next if $mode eq "120000" || $mode eq "160000";   # symlinks, submodules
  my $t = 631152000 + (hex(substr($blob, 0, 8)) % (1 << 30));
  utime($t, $t, $path) or warn "utime $path: $!\n";
'
find . -path ./.git -prune -o -type d -exec touch -h -d @631152000 {} +
