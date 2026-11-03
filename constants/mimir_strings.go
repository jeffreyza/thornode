package constants

// All strings used in Mimir keys should be recorded here and referred to from elsewhere,
// except for strings referring to arbitrary Assets/Chains.
// Each string should clearly indicate its usage for the final Mimir key (key, template, reference)
// and no Mimir key should require the combination of more than two strings.
const (
	MimirKeySecuredAssetHaltGlobal    = "HaltSecuredGlobal"
	MimirKeyWasmPermissionless        = "WasmPermissionless"
	MimirKeyWasmHaltGlobal            = "HaltWasmGlobal"
	MimirKeyWasmMinGasPrice           = "WasmMinGasPrice"
	MimirKeyScheduledMigration        = "ScheduledMigration"
	MimirKeyDisableTSSSignerFiltering = "DisableTSSSignerFiltering"
	// MimirKeySignatureRefreshBlocks tunes the bifrost vault keep-alive self-sign
	// staleness threshold in THORChain blocks (read by bifrost only, never by
	// consensus code). Unset defaults to ~1 day of blocks; 0 disables the keep-alive.
	MimirKeySignatureRefreshBlocks = "SignatureRefreshBlocks"
	MimirKeyStableReserveEnabled   = "StableReserveEnabled"
	// MimirKeySOLDurableNonce gates bifrost PROVISIONING of per-vault Solana
	// durable-nonce accounts, and nothing else. Read by bifrost only (never by
	// consensus code): >0 lets each active SOL vault create its nonce account.
	// Once a vault's nonce is committed on THORChain (by observer supermajority on
	// the observation of its creation), that vault signs every SOL outbound durably
	// from the committed value — anchored on the stored nonce (which never expires)
	// instead of a recent blockhash, removing the ~60s keysign+broadcast expiry
	// window — regardless of this mimir. Unset/0 leaves new vaults on the existing
	// recent-blockhash path.
	//
	// The feature is therefore one-way per vault: setting this back to 0 stops new
	// provisioning but does NOT return provisioned vaults to the recent-blockhash
	// path. That is required for safety — a legacy copy of an outbound does not
	// consume the nonce, so it and a still-valid durable copy could both land. The
	// operator emergency stop is HaltSigningSOL; the per-vault re-anchor is
	// MimirTemplateSOLNonceClear.
	MimirKeySOLDurableNonce = "SOLDurableNonce"

	// MimirKeySOLAnchorMarginSeconds tunes how far (seconds) bifrost steps back from
	// the THORChain keysign block time to pick the SOL consensus blockhash anchor.
	// Read by bifrost only, at the keysign height. Unset/<=0 = 15s; positive values
	// are clamped to [12, 30]. Lower leaves more of the ~150-block blockhash validity
	// for keysign + broadcast; below the fleet's finalization lag every node abstains.
	MimirKeySOLAnchorMarginSeconds = "SOLAnchorMarginSeconds"

	// MimirTemplateSOLNonceClear is the per-vault durable-nonce BREAK-GLASS
	// (SOLNonceClear-<vault pubkey>, value = a THORChain height H). A committed
	// SolVaultNonce with updated_height <= H is ignored everywhere (signers,
	// LackSigning, the query), so the vault signs on the recent-blockhash path. In
	// practice it stays legacy until it retires at churn: a voided vault sends no
	// durable txs and is not re-attested, so nothing commits a fresh nonce for it.
	// Use it to unwedge a vault whose committed nonce no longer matches its nonce
	// account and that automatic re-attestation cannot recover. It is a regular
	// (non-operational) mimir: it takes the normal 2/3 node vote.
	//
	// ⚠ Double-pay caveat: while the committed nonce is ignored, an outbound can be
	// re-issued (on this or another vault) although a durable copy signed with the
	// old value may still land. Set it only after confirming on-chain that the
	// committed nonce value is already consumed (the nonce account holds a newer
	// value), or with HaltSigningSOL in place.
	MimirTemplateSOLNonceClear = "SOLNonceClear-%s"

	// MimirKeyEnableMonovaultKeygen is a temporary, explicit rollout opt-in for
	// Bifrost XMR monovault keygen. It intentionally lacks an operational-Mimir
	// prefix, so mainnet activation requires the economic-Mimir supermajority.
	// Unset, zero, or query failure keeps keygen disabled.
	MimirKeyEnableMonovaultKeygen = "EnableMonovaultKeygen"
	// MimirKeyHaltMonovaultKeygen gates only the Bifrost XMR monovault FROST
	// keygen attempt after the explicit enable check, independently of
	// HaltChurning. The "Halt" prefix makes it an operational Mimir. Unset or 0
	// does not halt; an activation height at or below the current THORChain
	// height stops keygen.
	MimirKeyHaltMonovaultKeygen = "HaltMonovaultKeygen"

	// MimirKeyPreflightChainSetDisabled turns off the preflight check that keeps a
	// node out of the Ready set after it reported an asgard keygen chain set that
	// omitted a chain the active vaults serve. Set above 0 to disable the check.
	MimirKeyPreflightChainSetDisabled = "PreflightChainSetDisabled"
	// MimirKeyChainSetChurnOutMax arms the churn-out mark for Active nodes whose asgard
	// keygen report omitted a chain that a pending vault of this churn cycle was then
	// created without, and bounds it: when more nodes are faulty than this, none are
	// marked (a stall that many nodes cause is for governance). Unset or 0 disables it.
	MimirKeyChainSetChurnOutMax = "ChainSetChurnOutMax"

	MimirTemplateEVMAllowanceCheck         = "EVMAllowanceCheck-%s"         // Use with Chain
	MimirTemplateEVMDirectERC20Inbound     = "EVMDirectERC20Inbound-%s"     // Use with Chain
	MimirTemplateConfMultiplierBasisPoints = "ConfMultiplierBasisPoints-%s" // Use with Chain
	MimirTemplateMaxConfirmations          = "MaxConfirmations-%s"          // Use with Chain
	MimirTemplateSwapSlipBasisPointsMin    = "SwapSlipBasisPointsMin-%s"    // Use with MimirRef
	MimirTemplateSecuredAssetHaltDeposit   = "HaltSecuredDeposit-%s"        // Use with Chain
	MimirTemplateSecuredAssetHaltWithdraw  = "HaltSecuredWithdraw-%s"       // Use with Chain
	MimirTemplateTradeAssetHaltDeposit     = "HaltTradeDeposit-%s"          // Use with Chain
	MimirTemplateTradeAssetHaltWithdraw    = "HaltTradeWithdraw-%s"         // Use with Chain
	MimirTemplateWasmHaltChecksum          = "HaltWasmCs-%s"                // Encode the checksum to base32 to fit within mimir's 64 char limit and case insenstivity. Truncate trailing `=` for brevity
	MimirTemplateWasmHaltContract          = "HaltWasmContract-%s"          // Use contract address checksum (last 6) for brevity and to fit inside mimir's 64 char length
	MimirTemplateWasmHaltDeployer          = "HaltWasmDeployer-%s"          // Use full deployer address to prevent a deployer from instantiating new contracts
	MimirTemplateMaxGas                    = "MaxGas-%s"                    // Use with Chain (e.g., MaxGas-ETH)
	MimirTemplateRevShareThorName          = "REVSHARE-%s"                  // Use with thorname (e.g., REVSHARE-foobar)
	MimirTemplateSwitch                    = "EnableSwitch-%s-%s"           // Use with Chain, Symbol
	MimirTemplatePauseLPDeposit            = "PauseLPDeposit-%s"            // Use with Asset MimirString
	MimirTemplateOverSolvencyToTreasuryBps = "OverSolvencyToTreasuryBps-%s" // Use with Asset MimirString
	MimirTemplateHaltSigning               = "HaltSigning%s"                // Use with Chain (mixed case, e.g., HaltSigningETH)
	MimirTemplateHaltTrading               = "Halt%sTrading"                // Use with Chain (mixed case, e.g., HaltETHTrading)
	MimirTemplateSkipSignerCache           = "SkipSignerCache%s"            // Use with Chain (e.g., SkipSignerCacheBTC). Operational flag to allow signer cache bypass on expiry.
	MimirKeyHaltTradingGlobal              = "HaltTrading"                  // Global trading halt
	MimirTemplateAllowNoVault              = "AllowNoVault-%s"              // Use with Chain; authorizes noop:novault vault debits on that chain

	MimirRefL1           = "L1"           // Use with SwapSlipBasisPoints
	MimirRefSynth        = "Synth"        // Use with SwapSlipBasisPoints
	MimirRefTradeAccount = "TradeAccount" // Use with SwapSlipBasisPoints

	MimirMaxBatchSize           = "AttestationMaxBatchSize"
	MimirPeerConcurrentSends    = "AttestationPeerConcurrentSends"
	MimirPeerConcurrentReceives = "AttestationPeerConcurrentReceives"
)
