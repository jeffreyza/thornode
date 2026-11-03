#!/usr/bin/env bash
# PostToolUse(Edit|Write): format, regenerate, and lint the edited file.
# Problems go to stderr with exit 2 so Claude sees them; missing tools are skipped silently.
f=$(jq -r '.tool_input.file_path // empty')
[ -n "$f" ] && [ -f "$f" ] || exit 0
[[ $f == "$CLAUDE_PROJECT_DIR"/* ]] || exit 0
cd "$CLAUDE_PROJECT_DIR" || exit 0
has() { command -v "$1" >/dev/null; }

out=""
case "$f" in
*.go)
  if has goimports; then out=$(goimports -w "$f" 2>&1); fi
  ;;
*.proto)
  if has docker; then
    out=$(make proto-format proto-gen 2>&1) && out="" || out="make proto-format proto-gen failed:"$'\n'"$(tail -20 <<<"$out")"
  fi
  ;;
*/openapi/*.yaml | */openapi/*.yml)
  if has docker; then
    out=$(make openapi 2>&1) && out="" || out="make openapi failed:"$'\n'"$(tail -20 <<<"$out")"
  fi
  ;;
*/go.mod)
  out=$( (cd "$(dirname "$f")" && go mod tidy) 2>&1) && out=""
  ;;
*.gitlab-ci.yml)
  if has glab; then out=$(glab ci lint "$f" 2>&1) && out=""; fi
  ;;
esac

# trunk runs the same formatters and linters as CI (prettier, markdownlint, yamllint, shellcheck, shfmt, ...).
# Go is skipped here: golangci-lint per edit is too slow; CI and `make lint` cover it.
if [[ $f != *.go ]] && has trunk; then
  trunk fmt "$f" >/dev/null 2>&1
  chk=$(trunk check --no-fix --no-progress --color=false "$f" 2>&1) || out+=$'\n'"$chk"
fi

if [ -n "${out//[[:space:]]/}" ]; then
  perl -pe 's/\e\[[0-9;]*m//g' <<<"$out" >&2
  exit 2
fi
exit 0
