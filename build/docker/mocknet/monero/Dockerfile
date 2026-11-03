ARG DOCKER_MIRROR=
FROM ${DOCKER_MIRROR}ubuntu:24.04

# hadolint ignore=DL3008
RUN apt-get update \
  && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    jq \
    monero \
  && rm -rf /var/lib/apt/lists/*

COPY daemon-entrypoint.sh /usr/local/bin/daemon-entrypoint.sh
COPY wallet-entrypoint.sh /usr/local/bin/wallet-entrypoint.sh

RUN chmod +x /usr/local/bin/daemon-entrypoint.sh /usr/local/bin/wallet-entrypoint.sh

ENTRYPOINT ["/usr/local/bin/daemon-entrypoint.sh"]
