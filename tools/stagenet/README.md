# Custom Stagenet

This tooling provides simplified Docker Compose configuration to launch a custom `stagenet` network locally. There are also alternative ways to launch a custom `stagenet`, either via `node-launcher` on a Kubernetes cluster ([docs](https://gitlab.com/thorchain/devops/node-launcher/-/blob/master/docs/Custom-Stagenet.md?ref_type=heads)) or manually via other means. The tooling here is intended for lightweight temporary deployments.

## Single Validator Deployment

The following steps are a stripped down example to start a temporary local `stagenet`. The configuration by default has only Avalanche enabled as example.

**1. Install a `stagenet` build of `thornode` locally:**

```bash
# from the repo root
TAG=stagenet make install
```

**2. Add the "dog" mnemonic (the default faucet and admin mimir) to your local wallet:**

```bash
thornode keys add dog --recover
# enter the mnemonic: "dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
```

**3. Modify the `bifrost` configuration in `docker-compose.yml` to enable whatever chains you want.**

**4. Start the genesis node:**

```bash
docker compose --profile genesis up
```

**5. The local `stagenet` is live:**

- API: `http://localhost:1317`
- RPC: `http://localhost:27147`

## Adding Validators

**6. Start a second validator:**

```bash
docker compose --profile validator-1 up

# bond the validator (the address to use will be output in the logs)
thornode tx thorchain deposit 30000000000000 rune "BOND:sthor1uuds8pd92qnnq0udw0rpg0szpgcslc9ph3j6kf" --from dog --chain-id thorchain --node http://localhost:27147

# set ip/keys/version
docker compose exec validator-1-thornode /mnt/init-validator.sh
```

**7. Deposit coins into the vault of an enabled chain (pre-requisite to trigger churn) with an add memo:**

```text
ADD:AVAX.AVAX:sthor1zf3gsk7edzwl9syyefvfhle37cjtql3585mpmq
```

**8. Churn the network:**

```bash
thornode tx thorchain mimir ChurnMigrateRounds --from dog --chain-id thorchain --node http://localhost:27147 -- 2
thornode tx thorchain mimir ChurnInterval --from dog --chain-id thorchain --node http://localhost:27147 -- 100

# disable churning after success to prevent gas waste
thornode tx thorchain mimir HaltChurning --from dog --chain-id thorchain --node http://localhost:27147 -- 1
```

**9. Repeat steps 6-8 for additional validators. You can also add multiple nodes in a single churn.**

## Options

- Use custom seed phrases on any of the nodes by setting `SIGNER_SEED_PHRASE`.
- Use a custom faucet wallet and update `FAUCET`
- Add environment variables in `thornode`/`bifrost` services to override anything in `config/default.yaml`.
- Build and use your own custom `stagenet` image with local changes.
- Deploy custom router contracts and override the corresponding environment variables.

## Usage

This document and tooling provides a local `stagenet` setup to test pools, swaps, churns, savers and all other features as desired - however this usage is outside the scope of this document. Consult other documentation for an understanding of THORChain:

- https://dev.thorchain.org/
- https://docs.thorchain.org/

There are also many examples of transaction structure and logic flow in regression tests: https://gitlab.com/thorchain/thornode/-/tree/develop/test/regression/suites.
