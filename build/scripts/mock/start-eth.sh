#!/bin/sh

BLOCK_TIME="${BLOCK_TIME:=5}"

geth --dev --dev.period "$BLOCK_TIME" --verbosity 2 --datadir "data" -mine -http --http.addr 0.0.0.0 --http.port 8545 --http.api "eth,net,web3,miner,personal,txpool,debug" --http.corsdomain "*" -nodiscover --http.vhosts="*"
