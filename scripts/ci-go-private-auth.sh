#!/bin/sh
# Source this (". ./scripts/ci-go-private-auth.sh") in CI jobs that fetch the
# private module gitlab.com/thorchain/tss/tss-lib-private.
#
# It uses the first credential that can read tss-lib-private:
#   1. TSS_LIB_PRIVATE_READ_TOKEN (user TSS_LIB_PRIVATE_READ_USER): an optional
#      read-only deploy token, set as a CI/CD variable by a maintainer. Every
#      job of every pipeline that receives it can read it, including fork MR
#      pipelines run in this project, so prefer giving pipeline users Reporter
#      on tss-lib-private; if the token is used, make it protected and
#      short-lived.
#   2. CI_JOB_TOKEN, which only has the permissions of the user who started
#      the pipeline and so fails for anyone without access to tss-lib-private.
# The winner is configured for git in this job and exported as
# GO_PRIVATE_GIT_CREDENTIAL ("user:token"), which the Makefile forwards to
# docker builds as the go_private_auth BuildKit secret.
#
# Jobs that build in an empty dind daemon always fetch the module, so they
# set GO_PRIVATE_AUTH_REQUIRED=1 and the job stops here with an explanation.
# Otherwise it only warns: those jobs may still find the module in the
# runner's Go module cache.

go_private_auth_repo="https://gitlab.com/thorchain/tss/tss-lib-private.git"

# go_private_auth_probe <user:token>: 0 if it can read the repo, 1 if it is
# refused, 2 if gitlab.com could not be reached.
go_private_auth_probe() {
  _attempt=1
  while :; do
    _err=$(GIT_TERMINAL_PROMPT=0 git ls-remote --exit-code \
      "https://$1@gitlab.com/thorchain/tss/tss-lib-private.git" HEAD 2>&1 >/dev/null) && return 0
    case "$_err" in
    *"Authentication failed"* | *"Access denied"* | *"not found"* | *"could not be found"* | *" 401"* | *" 403"* | *" 404"* | *"could not read Username"*)
      return 1
      ;;
    esac
    if [ "$_attempt" -ge 3 ]; then
      echo "$_err" | sed 's/:[^:@]*@/:***@/g' >&2
      return 2
    fi
    _attempt=$((_attempt + 1))
    sleep 5
  done
}

go_private_auth() {
  _unreachable=false
  for _source in dedicated job; do
    case "$_source" in
    dedicated)
      [ -n "${TSS_LIB_PRIVATE_READ_TOKEN-}" ] || continue
      # A deploy token needs its own username; access tokens accept any.
      _cred="${TSS_LIB_PRIVATE_READ_USER:-oauth2}:${TSS_LIB_PRIVATE_READ_TOKEN}"
      _name="TSS_LIB_PRIVATE_READ_TOKEN"
      ;;
    job)
      [ -n "${CI_JOB_TOKEN-}" ] || continue
      _cred="gitlab-ci-token:${CI_JOB_TOKEN}"
      _name="CI_JOB_TOKEN (permissions of ${GITLAB_USER_LOGIN:-the pipeline user})"
      ;;
    esac
    # GitLab runs job scripts with errexit, so never call the probe bare.
    _rc=0
    go_private_auth_probe "$_cred" || _rc=$?
    case $_rc in
    0)
      echo "tss-lib-private: using $_name"
      git config --global url."https://${_cred}@gitlab.com/thorchain/tss/".insteadOf "https://gitlab.com/thorchain/tss/"
      GO_PRIVATE_GIT_CREDENTIAL="$_cred"
      export GO_PRIVATE_GIT_CREDENTIAL
      return 0
      ;;
    1) echo "tss-lib-private: $_name cannot read $go_private_auth_repo" ;;
    *) _unreachable=true ;;
    esac
  done

  if $_unreachable; then
    echo "tss-lib-private: gitlab.com did not answer; retrying the job may help."
  fi
  echo "tss-lib-private: no credential can read $go_private_auth_repo."
  echo "  CI_JOB_TOKEN has the permissions of whoever started the pipeline"
  echo "  (${GITLAB_USER_LOGIN:-unknown}). Either that user needs at least Reporter"
  echo "  access to thorchain/tss/tss-lib-private, or a maintainer must set the"
  echo "  TSS_LIB_PRIVATE_READ_USER and TSS_LIB_PRIVATE_READ_TOKEN CI/CD variables"
  echo "  (a read_repository deploy token on tss-lib-private)."
  if [ "${GO_PRIVATE_AUTH_REQUIRED-}" = "1" ]; then
    if $_unreachable; then
      exit 75
    fi
    exit 1
  fi
  # Keep the old behaviour so a warm module cache still works.
  if [ -n "${CI_JOB_TOKEN-}" ]; then
    git config --global url."https://gitlab-ci-token:${CI_JOB_TOKEN}@gitlab.com/thorchain/tss/".insteadOf "https://gitlab.com/thorchain/tss/"
  fi
  return 0
}

go_private_auth
