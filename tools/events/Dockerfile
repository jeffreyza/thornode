########################################################################################
# Build
########################################################################################

ARG DOCKER_MIRROR=
FROM ${DOCKER_MIRROR}golang:1.26.10-bookworm AS build

ENV GOBIN=/go/bin
ENV GOPATH=/go
ENV GOOS=linux
# tss-lib-private is a private GitLab subgroup repo; skip proxy.golang.org/sumdb
# so `go mod download` resolves it via the git insteadOf below.
ENV GOPRIVATE=gitlab.com/thorchain/tss/tss-lib-private.git

WORKDIR /app
COPY go.mod go.sum ./
# Authenticate the private fetch with the go_private_auth BuildKit secret
# ("user:token", chosen by scripts/ci-go-private-auth.sh in CI).
# required=false keeps local `make` builds working without it.
# File-based mount instead of env= so hadolint 2.12.1-beta can parse the RUN.
RUN --mount=type=secret,id=go_private_auth,required=false \
    if [ -s /run/secrets/go_private_auth ]; then \
      git config --global url."https://$(cat /run/secrets/go_private_auth)@gitlab.com/thorchain/tss/".insteadOf "https://gitlab.com/thorchain/tss/"; \
    fi && \
    go mod download && \
    rm -f /root/.gitconfig

COPY . .
ARG TAG=mainnet
RUN go install -tags $TAG ./tools/events

########################################################################################
# Deploy
########################################################################################

FROM ${DOCKER_MIRROR}debian:bookworm-20260610-slim@sha256:96e378d7e6531ac9a15ad505478fcc2e69f371b10f5cdf87857c4b8188404716

COPY --from=build /go/bin/events /usr/bin/
COPY --from=build /go/pkg/mod/github.com/!cosm!wasm/wasmvm/v2@v2.2.9/internal/api/libwasmvm.*.so /usr/lib

RUN apt-get update && \
  apt-get install -y --no-install-recommends curl=7.88.1-10+deb12u* ca-certificates=20250419~deb12u2 && \
  rm -rf /var/lib/apt/lists/*

CMD ["events"]