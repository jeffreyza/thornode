# Review GitLab Merge Request

Review the GitLab merge request: $ARGUMENTS

IMPORTANT: This is a blockchain codebase. Consensus safety is the HIGHEST priority.

## Review Workflow

### Phase 1: Gather Information (Run in Parallel)

Execute these commands in parallel for efficiency:

- `glab mr view` - Get MR details and metadata
- `glab mr diff` - Get code changes
- `glab ci view` - Check pipeline status
- `git log origin/develop..HEAD` - View commit history

### Phase 2: Automated Checks

Before manual review, verify automated tooling:

1. Check CI/CD pipeline status - all checks must pass
2. Verify `make lint` passes
3. Verify `goimports -w` formatting is correct
4. If .proto files changed, verify `make proto-format` was run
5. If CI fails, stop and report - don't review until green

### Phase 3: Consensus Safety Review (CRITICAL)

Invoke the `blockchain-code-review` skill on the diff. It owns the pattern catalogue (non-determinism, panics in block lifecycle, arithmetic, version gating, protobuf, WASM queries); do not re-derive it here. Carry its findings into Phase 7 unchanged.

### Phase 4: Security & Bug Review

Focus on HIGH-VALUE findings only. DO NOT comment on:

- Style/formatting issues (handled by tools)
- Trivial naming suggestions
- Minor refactoring ideas
- Cosmetic improvements

DO focus on:

- **Security vulnerabilities**: Authentication, authorization, input validation, cryptography
- **Logic bugs**: Off-by-one errors, nil pointer dereferences, race conditions
- **Data integrity issues**: State corruption, incorrect calculations, lost data
- **Error handling gaps**: Unhandled errors, silent failures, improper error propagation
- **Performance problems**: O(n²) algorithms, unnecessary loops, memory leaks
- **Code smells**: Duplicated logic, overly complex functions, tight coupling

### Phase 5: Test Quality Review

Evaluate test coverage and quality:

1. Are new functions/handlers covered by unit tests?
2. Do tests cover edge cases (zero values, nil, empty, overflow)?
3. Are regression tests updated for protocol changes?
4. Do tests actually test the right behavior (not just code coverage)?
5. Are there any flaky test patterns (time-dependent, order-dependent)?

### Phase 6: Impact Analysis

Search codebase for potential ripple effects:

1. Use Grep to find all callers of modified functions
2. Check for interface changes affecting multiple implementations
3. Verify backward compatibility for protocol upgrades
4. Check for breaking changes in public APIs

### Phase 7: Draft Review (Structured Output)

Organize findings by severity using this format:

```markdown
## 🚨 CRITICAL (Consensus-Breaking)

[Issues that could cause chain halt or consensus failure]

- file.go:123 - [description with clear explanation]

## ⚠️ HIGH (Security/Data Integrity)

[Security vulnerabilities, data corruption risks, major bugs]

- file.go:456 - [description]

## 📊 MEDIUM (Logic/Performance)

[Logic bugs, performance issues, error handling gaps]

- file.go:789 - [description]

## 💡 LOW (Code Quality)

[Code smells, maintainability concerns - only if significant]

- file.go:101 - [description]

## ✅ Positive Notes

[Highlight good practices, clever solutions, improvements]

## 📋 Summary

- X CRITICAL issues found (MUST fix before merge)
- Y HIGH severity issues (should fix before merge)
- Z MEDIUM severity issues (fix or document why safe)
- Overall recommendation: [APPROVE / REQUEST CHANGES / BLOCK]
```

### Phase 8: Present for Approval

Show the complete review with:

1. Total count by severity
2. All proposed inline comments with file:line locations
3. Overall recommendation and rationale
4. **Ask for explicit approval before posting anything**

### Phase 9: Post Review (After Approval Only)

Comments go inline on the changed line, not as general MR notes. `glab mr note` cannot do this (it has no position flag), so use the discussions API. Get the diff refs once:

```bash
glab mr view <MR_ID> --output json | jq '.diff_refs'   # base_sha, head_sha, start_sha
```

Then, per finding:

```bash
glab api --method POST -H "Content-Type: application/json" \
  "projects/:fullpath/merge_requests/<MR_ID>/discussions" --input - <<'EOF'
{
  "body": "<comment>",
  "position": {
    "position_type": "text",
    "base_sha": "<base_sha>", "head_sha": "<head_sha>", "start_sha": "<start_sha>",
    "new_path": "<file>", "old_path": "<file>",
    "new_line": <line>
  }
}
EOF
```

`position` must be a nested object, so it has to go through `--input`: `--field` and `--raw-field` send every value as a string and never parse JSON objects, which posts the comment unpositioned. `--input` also needs the explicit `Content-Type` header, or GitLab answers `415`. Use `old_line` instead of `new_line` for a comment on a removed line. Post the severity summary as one general note only after the inline comments. Do not approve, request changes, or change labels without explicit permission.

## Review Principles

✅ **DO:**

- Be thorough on critical issues (consensus, security, bugs)
- Provide specific file:line references
- Explain WHY something is a problem and the impact
- Suggest concrete fixes with code examples
- Verify claims by reading related code
- Run tests locally if uncertain about behavior

❌ **DON'T:**

- Comment on style/formatting (tools handle this)
- Suggest subjective improvements without clear benefit
- Review line-by-line for trivial issues
- Approve without user permission
- Post comments without user approval
- Post findings as general notes when they belong on a line

## Context for LLM

- This is a Go/Cosmos SDK blockchain codebase
- Consensus safety > security > bugs > performance > style
- Use glab CLI for all GitLab operations
- Tests run with `make test`, regression tests with `make test-regression`
- Format code with `goimports -w`, protos with `make proto-format`
