FROM rust:1.80-bookworm AS build

ARG AGAVE_TAG=v2.3.8

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
  git \
  clang \
  llvm \
  libclang-dev \
  pkg-config \
  libssl-dev \
  protobuf-compiler \
  cmake \
  libudev-dev \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /src
RUN git clone --depth=1 --branch="$AGAVE_TAG" https://github.com/anza-xyz/agave.git . && \
  cargo build --release \
  --bin solana-test-validator \
  --bin solana-genesis \
  --bin solana \
  --bin solana-keygen


FROM debian:bookworm-slim AS runtime

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
  ca-certificates \
  libssl3 \
  libudev1 \
  && rm -rf /var/lib/apt/lists/*

COPY --from=build /src/target/release/solana-test-validator /usr/local/bin/
COPY --from=build /src/target/release/solana-genesis /usr/local/bin/
COPY --from=build /src/target/release/solana /usr/local/bin/
COPY --from=build /src/target/release/solana-keygen /usr/local/bin/
