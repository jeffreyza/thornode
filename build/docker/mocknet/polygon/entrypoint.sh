#!/bin/sh

echo "" | bor account import --datadir /root/.bor /mocknet/priv.key
bor "$@"
