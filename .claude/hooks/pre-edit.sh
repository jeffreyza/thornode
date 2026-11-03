#!/usr/bin/env bash
# PreToolUse(Edit|Write): refuse edits to generated files. Exit 2 sends stderr back to Claude.
f=$(jq -r '.tool_input.file_path // empty')
case "$f" in
*.pb.go | *.pb.gw.go | *.pulsar.go | */openapi/gen/* | */test/regression/mnt/*)
  echo "Blocked: $f is generated. Edit the source (.proto, openapi/openapi.yaml, regression suite) and regenerate." >&2
  exit 2
  ;;
esac
exit 0
