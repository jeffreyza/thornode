FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

# Official PIVX release. v5.6.1 ships a glibc-linked pivxd with no extra shared
# libraries, so the bookworm runtime is enough.
ARG PIVX_VERSION=5.6.1
ARG TARGETARCH
RUN set -eux; \
    case "${TARGETARCH}" in \
      amd64) asset="x86_64-linux-gnu" ;; \
      arm64) asset="aarch64-linux-gnu" ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/pivx.tar.gz \
      "https://github.com/PIVX-Project/PIVX/releases/download/v${PIVX_VERSION}/pivx-${PIVX_VERSION}-${asset}.tar.gz"; \
    tar -xzf /tmp/pivx.tar.gz -C /opt; \
    mv "/opt/pivx-${PIVX_VERSION}" /opt/pivx; \
    ln -s /opt/pivx/bin/pivxd /usr/local/bin/pivxd; \
    ln -s /opt/pivx/bin/pivx-cli /usr/local/bin/pivx-cli; \
    rm /tmp/pivx.tar.gz

COPY build/docker/mocknet/pivx/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 51473
ENTRYPOINT ["/entrypoint.sh"]
