FROM registry.gitlab.com/thorchain/devops/node-launcher:bitcoin-daemon-29.1 as bitcoin
FROM registry.gitlab.com/thorchain/devops/node-launcher:bitcoin-cash-daemon-29.0.0 as bitcoin-cash
FROM registry.gitlab.com/thorchain/devops/node-launcher:dogecoin-daemon-1.14.9 as dogecoin
FROM registry.gitlab.com/thorchain/devops/node-launcher:litecoin-daemon-0.21.4 as litecoin

ARG DOCKER_MIRROR=
FROM ${DOCKER_MIRROR}golang:1.26.10 AS build

# build geth since the image is alpine based
RUN go install github.com/ethereum/go-ethereum/cmd/geth@v1.16.3

# copy chain clis
COPY --from=bitcoin /usr/local/bin/bitcoin-cli /usr/local/bin/bitcoin-cli
COPY --from=bitcoin-cash /usr/local/bin/bitcoin-cli /usr/local/bin/bitcoin-cash-cli
COPY --from=dogecoin /usr/local/bin/dogecoin-cli /usr/local/bin/dogecoin-cli
COPY --from=litecoin /usr/local/bin/litecoin-cli /usr/local/bin/litecoin-cli

# tss-lib-private is a private GitLab subgroup repo; skip proxy.golang.org/sumdb
# so `go mod download` resolves it via the git insteadOf below.
ENV GOPRIVATE=gitlab.com/thorchain/tss/tss-lib-private.git

WORKDIR /app
COPY go.mod go.sum ./
# Authenticate the private fetch with the go_private_auth BuildKit secret
# ("user:token", chosen by scripts/ci-go-private-auth.sh in CI).
# required=false keeps local `make` / `docker compose` builds working without it.
# File-based mount instead of env= so hadolint 2.12.1-beta can parse the RUN.
RUN --mount=type=secret,id=go_private_auth,required=false \
    if [ -s /run/secrets/go_private_auth ]; then \
      git config --global url."https://$(cat /run/secrets/go_private_auth)@gitlab.com/thorchain/tss/".insteadOf "https://gitlab.com/thorchain/tss/"; \
    fi && \
    go mod download && \
    rm -f /root/.gitconfig

COPY . .

ARG TAG=mocknet
RUN make install