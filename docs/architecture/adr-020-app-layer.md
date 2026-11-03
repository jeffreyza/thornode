# ADR 020: App-layer

## Changelog

- May 2025: Proposed

## Status

Proposed

## Context

THORChain's SDK V50 update in V3.0.0 (https://gitlab.com/thorchain/thornode/-/merge_requests/3756) enabled it to support an "app-layer" - a separate execution environment. Addresses are allowed to point to RUST-based WASM code that execute deterministically in the transaction life-cycle and can trigger other messages.

This ADR describes the Design, Security, Guidelines, Emergency Procedures and Deployment Process of the App Layer. It can be used by the community as the definitive guide to how the App Layer should operate.

## Alternative Approaches

1. No App Layer - THORChain's historical position; attempting to build features as complete handlers/managers in the base layer. The key problem with this is developer bottleneck - since base layer code can easily touch sensitive vault/security logic, base-layer development is slow and cautious.

2. Hosted App Layer - this design - where COSMWASM is deployed and hosted by the THORNode binary. Contracts execute as part of THORChain's state transitions for each block.

3. Remote App Layer - using IBC and the [Cosmos Interchain Accounts](https://tutorials.cosmos.network/academy/3-ibc/8-ica.html) feature - a remote app layer hosted by external validators is possible. Users deposit IBC assets into a remote chain, and remote validators settle periodic state updates back to THORChain.

## Decision - Hosted App Layer

A Hosted App Layer is most appropriate for THORChain currently:

1. WASM contracts with strict boundary conditions allow for a variety of apps to be developed quickly and easily by developers with low risk to the Base Layer. They can also be called by L1 wallets easily - allowing for seamless UX.
2. A Remote App Layer is not ideal for seamless L1 experiences due to a 2-step deposit process. A Remote App Layer for a specific high-use app may be entertained in future, eg Perps, where blocktimes, state bloat and more limit the UX of a Hosted App.

## Detailed Design

### WASM

The `x/wasm` module is implemented with the following features:

1. `MsgExecuteContract` can be called by any address to any other address, which can run logic and call:`MsgSend` or `MsgDeposit` (or recursively call another contract)
2. Permissioning, where only whitelisted deployer addresses can deploy whitelisted bytecode - this ensures a basic due diligence and quality control process.
3. Control - where Node Mimir can pause any contract, any set of contracts or the entire app layer

```go
HaltWasmGlobal
HaltWasmCs-%s
// Encode the checksum to base32 to fit within mimir's 64 char limit and case insenstivity. Truncate trailing `=` for brevity
HaltWasmContract-%s
// Use contract address checksum (last 6) for brevity and to fit inside mimir's 64 char length
WASMPERMISSIONLESS
// Enable permissionless WASM contracts
```

### Secured Assets

Contracts can move Secured Assets, which are wrapped L1 assets, represented in THORChain's state as native `x/bank` assets:

1. Secured Assets are denomed like `btc-btc` for `BTC.BTC` and minted natively.
2. Secured Assets are not directly redeemed for vaulted assets, they are managed as a "share". This allows negative interest for exported assets to be entertained in future.
3. Secured Assets have all the features that Trade Assets have (swaps etc)
4. Mint with `secure+:thorAddress` and Redeem with `secure-:l1Address`
5. Node Pauseable

```go
HaltSecuredGlobal
HaltSecuredDeposit-%s
// Use with Chain
HaltSecuredWithdraw-%s
// Use with Chain
```

Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3721

### Token Factory

Contracts can mint using the [Token Factory](https://docs.osmosis.zone/osmosis-core/modules/tokenfactory/) - with the `x/denom` module.

1. Only WASM contracts can create tokens: `MsgCreateDenom`. Since contracts are permissioned, this will stop token-spam.
2. Currently X tokens are blocked as Base Layer pools - liquidity can only be provided to them on the App Layer itself. This will be reviewed in future.

Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3837

### Switch Handler

Tokens can one-way migrate to the App Layer using the Switch Handler.

1. Devs provide a source token, burn address and a target denom for each whitelisted `CHAIN-ASSET`
2. Users deposit tokens with a `SWITCH:thorAddress`. Source tokens are forever discarded and target tokens are minted to the address.
3. Nodes currently use mimir to whitelist tokens that can switch (note, in future this may be relaxed to base-layer PRs to allow more tokens to migrate faster without having to be handled by Nodes)

```go
EnableSwitch-%s-%s
// Chain, Asset
```

Implemented as per https://gitlab.com/thorchain/thornode/-/merge_requests/3840

## App Security

The App Layer is designed with a strict separation of concerns from THORChain’s Base Layer, ensuring it introduces no new systemic risk. Contracts deployed on the App Layer are sandboxed with limited capabilities, functioning similarly to externally owned accounts (EOAs), with limits on what they can call, query and use.

To mitigate risk the following security measures are taken for every contract:

- Audits: All deployed contracts undergo internal reviews from core team members and third-party audits before mainnet launch.
- Permissioned Deployment: Only pre-approved contract bytecode and whitelisted deployer addresses are allowed on mainnet.
- Operational Controls: THORChain’s Mimir governance can pause individual apps, all apps, or secured asset minting/burning in response to threats.
- Impact Isolation: If an exploit occurs, only the users of the affected app are impacted—the Base Layer and its liquidity pools remain unaffected.
- No Elevated Privileges: Contracts cannot mint or burn secured assets or interfere with Base Layer consensus. Their rights are no greater than any regular user with a thor address.

This design ensures that even in the event of a compromised application, THORChain’s integrity, solvency, and cross-chain operations remain secure.

### Call Limits

- `MsgCreateDenom`, eg to mint a Token to represent fungible units
- `MsgSend`, to send an asset to another address
- `MsgDeposit`, to call into THORChain's base layer, ie, to swap, bond, redeem
- `MsgExecuteContract`, to call into another contract

### Query Limits

- Only specific base layer query endpoints are made available to the App Layer to prevent possible non-deterministic queries (historically has lead to DoS attacks on other chains)

### Asset Limits

- Only native assets `rune`, secured assets `btc-btc` and X assets (Token Factory) can be moved by contracts

In short - apps are like "addresses that can do things" - they do not have any extra privilege that a RUNE address can do today (with the exception of `MsgCreateDenom`, which is only designed to prevent Token Spam by users).

## Economic Security

Secured Assets are optimistically secured by a Security Budget set by nodes as a `TVLCapBasisPoints`, which allows depositing of assets (Pool, Trade, Secured) up to a limit defined against the Total Active Bond.

> A `TVLCapBasisPoints = 10_000 bps` means total TVL is allowed to equal the Bond.
> Eg, 100m RUNE Bonded at $1.00, with 50m RUNE in the pools and $10m total as trade assets, allows 40m RUNE ($40m) to be the budget for the App Layer.

As long as apps generate fees on Secured Assets and a fee-share is paid to Nodes, then Nodes will bond-up at some risk-free rate to continuously open up space for more Secured Assets.

Implemented as https://gitlab.com/thorchain/thornode/-/merge_requests/3972

### App-specific security

Currently all apps pay a global fee-share and use assets in the global budget. But not all apps are the same.

In future a more granular market-based approach might be taken where apps rent a security budget (Staked RUNE) and pay a fee-share directly to their stakers. The staked RUNE per app sets the per-app TVL caps, and the app can offer any fee-share level they want. The Staked RUNE per app is the slashable security.

This idea will be re-visited after some time of the App Layer operating.

## App Layer Guidelines

This section sets out general operating principles that should guide Devs, THORNodes and the community in handling and growing the App Layer.

### Quality & Due Diligence

Apps should be high-quality, audited, tested and have UX support before being presented for base layer whitelisting, with the following aspects:

- Differentiation: The application must deliver a unique or novel use case that builds meaningfully on the core primitives provided by existing apps, rather than replicating existing solutions.
- Team: The development team must demonstrate a verifiable track record of shipping reliable, secure applications—ideally within the specific domain the app targets. Public contributions, community trust, and prior deployments are considered key signals.
- Review: Every app must undergo an internal technical review by the existing contributors to evaluate architectural soundness, integration practices, and adherence to security best practices, as well as an audit by an independent third-party security firm, with public results and follow-up remediation prior to mainnet deployment.

### Fee-sharing

HORNodes provide security, L1 observation, decentralisation and execution of app-layer contracts and assets; which is significantly expensive. Transaction fees are not enough to pay for this, since users generally object to high transaction fees ($1.00 and above). THORChain has set tx fees currently to 0.02 RUNE.

Instead - Apps should offer fee-sharing to THORNodes directly from their service fees; eg

- Interest fees on lending/borrowing/stablecoins
- Trade/Liquidity fees from liquidity protocols
- Listing/Sale fees on marketplaces
  etc

To do so, ensure that the share of fees collected in an app are swapped to RUNE and sent to the RESERVE Module address:

```plaintext
thor1dheycdevq39qlkxs2a6wuuzyn4aqxhve4qxtxt
```

Importantly, there are nuances between types of apps/projects that might require different fee-sharing arrangements. The core logic regarding fee sharing is the following:
For each transaction on the App Layer, is value accrued to the Base Layer?

- If YES, there is no need to share revenue (the Base Layer already benefits).
- If NO, then the 50/50 revenue split applies to pay for security.

### 12-month non-compete and fee sharing revision

The Rujira team has spent a lot of time completing foundational work to make an app layer on THORChain possible at all, and building all the dev tooling and components that will make it easy for competing apps to launch. Rujira will be given a 12-month exclusivity period, which started from the moment THORChain v3.6 went live on 22-May-2025, and will end on 22-May-2026.

At the comclusion of the exclusivity period the community will vote on whether to extend the exclusivity, or open it up to other apps. This will likely be judged by the performance of Rujira (fees, users, apps) in the preceding 12 months. If the exclusivity is relaxed, then the base-layer should also drop the fee-sharing, likely down to 30% or lower. This will encourage more developers to build on the App-layer.

### Builders’ classification

Building on this premise, we suggest categorizing builders into 6 categories with corresponding guidance for fee-sharing:

1. Rujira Alliance: This includes the apps built (and to be built) by the teams that joined the Rujira Alliance (Kujira, Levana, Fuzion, Gojira). Those constitute the Core Apps and include: orderbook DEX, AMM for the orderbook, Perps, Money market, BTC-backed stablecoin, Liquidations, Launchpad, Options, NFT marketplace and Prediction market. More teams might be invited to join the Rujira Alliance if they can offer something new and differentiated. Revenue from core apps is shared 50/50 with the Base Layer (except for the apps that execute base layer swaps).

2. Value Additive to Core Apps: Projects building on top of existing core apps. Provides value-added services to users by enabling advanced features or simplifying complex processes. Those projects increase economic activity on top of core apps and are very welcome to launch as independents on the App Layer. Building on top of Rujira core apps means that those projects are already paying fees to RUJI stakers and TC Base Layer, therefore those projects get to keep 100% of their revenue.

3. Differentiated - Out of Scope: Projects offering something new, not available on any of the existing core apps, and considered “non-strategic” or “out of scope” by the Rujira Team. Those projects won’t be joining the Rujira Alliance but are welcome to launch as independents. They get the same fee sharing arrangement that Rujira has at that moment in time.

4. Competing with Core Apps: Projects doing something similar to any of the Rujira core apps or existing projects. After an initial 12-month non-compete period for Rujira core apps, those projects will be allowed to launch to foster healthy competition. They get the same fee sharing arrangement that Rujira has at that moment in time.

5. Public Goods: Projects that do not directly generate economic activity/revenue but are perceived as net positive for the ecosystem. This includes things like tooling, analytics and education. Not expected to generate any revenue, but mentioned for completeness.

6. Real World Business: In the long run, we expect to see real world businesses that decide to operate on blockchain rails because it provides them with the ability to raise capital, access real-time accounting, better payment solutions, etc. at a fraction of the cost of running a traditional business. We want them to choose THORChain App Layer because it’s the best ecosystem to access all those things, from all connected chains. We want them to fuel a thriving economic activity on top of THORChain. Imposing a 50% taxation on those businesses would stifle adoption and deter participation. Instead, they will get to keep 100% of their revenue and will generate value for THORChain and Rujira by using the core apps (e.g. raising capital on RUJI Ventures, getting their token traded on RUJI Trade, borrowing on RUJI Lending, etc.).

### No Uncapped Systemic-risk Apps

A systemic-risk app is defined as an app that could accumulate large uncapped quantities of RUNE. Such an app could be RUNE-denominated borrow/lend, or RUNE-bond LSTs.
These apps may be entertained only if a safe cap is enforced - a suggested cap would be no more than 10% of either the Active Bond or on-chain RUNE Liquidity.

> Eg with 100m RUNE bonded and 50m RUNE in liquidity, a sensible LST cap would be 10m RUNE (10% of Active Bond) and a sensible RUNE collateral cap would be 10% of the liquidity - 5m RUNE.

### Diverse Apps Encouraged

Apps are generally more successful if there is clear differentiation, since it attracts new users and prevents developer angst and liquidity fragmentation. Diversity of apps should be encouraged during the due diligence process of base-layer whitelisting.

Directly cloned apps by different teams are not encouraged but will be permissible post non-compete period.

Similar apps targeting the same vertical are encouraged for healthy competition as long as quality is high and intent genuine.

### App Upgradeability

Since whitelisted deployers can only deploy whitelisted contracts after having adequate base-layer due diligence, it is encouraged that all apps have retain deployer upgradeability.

Upgrading contracts and ensuring state and contract-address stay the same allows for bug-fixes and improvements to be seamlessly and securely rolled out.

More info: https://github.com/CosmWasm/cosmwasm

### Contentious Apps

If an app becomes Contentious to the base-layer (as decided by THORNodes), then THORNodes can pause the app and request the dev address issues. Other nodes can join the pause or unpause if they disagree.
Contentious apps could be any app becoming a risk to the protocol or its users. There is no clear category of apps that could meet this definition, it is left up to THORNodes to decide at the time.

> Apps in breach of these Guidelines are contentious if the breach is easily proven.

> Apps becoming a risk to the protocol and its users may be deemed contentious (amassing large amounts of assets with poor security or un-aligned developer interests, eg, rugs or scams)

> Privacy apps, dark-net marketplaces, betting platforms and more may be contentious if adequate controls are not put in place by the app-dev. "Adequate controls" should be determined as per other apps in other protocols.

## Emergency Procedures

App-devs, Base-devs and THORNodes are responsible for responding to and managing security issues.

> THORChain does not subscribe to the philosophy that "code is forever law" - rather that the chain and assets are sovereign and that long term a safe and vibrant ecosystem will bring more users and fees - thus exploits can and should be handled. THORChain's App Layer implementation allows contracts to be upgraded by a review process by neutral Base Layer Devs.

### Vulnerability Reported

If a P0 is reported, but no exploit yet, THORNodes can:

1. Pause the affected app, all copies of the app, or the entire app-layer
2. `make relay` the reason
3. Base Devs work with App devs to roll-out an app-upgrade

### Active Exploit

If an app is exploited and user assets are being moved, THORNodes can:

1. Pause the entire chain `make halt` which will pause everything, including `MsgSend` and Secured Assets withdrawals
2. Specifically identify the exploit and target more granular pauses (if applicable)
3. Base-devs to work with App-devs to roll-out an app-upgrade, and possible store-migrations to move stolen assets back.

> There is a financial threshold that an exploit would entertain a widespread halt and upgrade. A $50 exploit would not call for it, but a $5m exploit would. It is left up to THORNodes to make their own decisions to pause/unpause.

### App Upgrades

Apps that should be upgraded after a reported vulnerability or exploit should follow this process:

1. Submit the new app bytecode that migrates user-state correctly and addresses any issues to the Base Layer Devs
2. Apps that can't migrate can have their state and assets store-migrated to new contracts.

## Deployment

Devs wishing to deploy an App on THORChain should read this ADR. Any and all apps can be entertained.

1. Build an app, conduct devnet testing and auditing.
2. Ensure the app meets the App Layer Guidelines
3. Submit a finalised contract bytecode, deployer address and origin link (containing app description and documentation) to the Base Layer Tech lead for review and merging to the Base Layer.
4. Deploy (3) on mainnet once approved
5. Be ready to handle feedback from THORNodes on app features and issues, as well as managing upgrades.

## Rujira

[Rujira](https://gitlab.com/thorchain/rujira) is a well-funded alliance of apps with a cohesive brand, developer community and [developer tools](https://gitlab.com/thorchain/rujira-ui) that is likely to deploy and build the largest collection of apps on THORChain.

Due to developer support, devnet tooling, audit-funding and more, app-devs are encouraged to be handled by the Ruji team. [More info can be found here](https://docs.rujira.network/developers/getting-started).

RUJI is not exclusive to THORChain, and apps can be deployed outside of the RUJI Alliance if the app-dev wishes to support themselves on tooling, funding, testing and deployment. As long as an app meets the App Layer Guidelines and passes Base Layer Dev due diligence, then it can be deployed and hosted.

### Summary

This ADR details the App-layer as of 2025. Some or all parts of this ADR may be superseded by an ADR update if needed.
