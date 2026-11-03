#!/bin/bash
set -euo pipefail

echo "Linting handlers file"

get_instances() {
  PATTERN="$1"
  shift
  # Files with no matches cause grep to return error code.
  grep --no-filename -E -o "$PATTERN" "$@" || true
}

while IFS= read -r -d '' f; do
  if [[ $f == *"_test"* ]]; then
    continue
  fi
  if [[ $f == *"_archive"* ]]; then
    continue
  fi
  base=${f%.*}
  archive="$base"_archive.go
  files=("$f")
  if [[ -f $archive ]]; then
    files+=("$archive")
  fi

  validate_init=$(get_instances " validateV[0-9]+" "${files[@]}")
  validate_call=$(get_instances "\.validateV[0-9]+" "$f")

  handler_init=$(get_instances " handleV[0-9]+" "${files[@]}")
  handler_call=$(get_instances "\.handleV[0-9]+" "$f")

  missing=$(echo -e "$validate_init\n$validate_call\n$handler_init\n$handler_call" | sed -e 's/^.//g' | sort -n | uniq -u)

  if [[ -n $missing ]]; then
    echo "Handler: $f... Failed"
    echo "Detected but not used:"
    echo "$missing"
    exit 1
  fi
  echo "Handler: $f... OK"
done < <(find x/thorchain/ -name "handler_*.go" -print0)
