package tss

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type RecoverKeysharesSuite struct{}

var _ = Suite(&RecoverKeysharesSuite{})

func (s *RecoverKeysharesSuite) TestRecoverScanLowerBound(c *C) {
	c.Assert(recoverScanLowerBound(0, 100), Equals, int64(0))
	c.Assert(recoverScanLowerBound(50, 100), Equals, int64(0))
	c.Assert(recoverScanLowerBound(500, 100), Equals, int64(400))
}

func (s *RecoverKeysharesSuite) TestRecoverScanBoundsIncludesBoundedForwardWindow(c *C) {
	lower, upper := recoverScanBounds(648, 680, 100)
	c.Check(lower, Equals, int64(548))
	c.Check(upper, Equals, int64(680))

	lower, upper = recoverScanBounds(648, 900, 100)
	c.Check(lower, Equals, int64(548))
	c.Check(upper, Equals, int64(748))

	lower, upper = recoverScanBounds(648, 600, 100)
	c.Check(lower, Equals, int64(548))
	c.Check(upper, Equals, int64(648))
}

func (s *RecoverKeysharesSuite) TestApplyPruneFloor(c *C) {
	// Whole range below the node floor → unrecoverable, lower bound unchanged.
	// (legacy vault ~17.4M vs node floor ~20.4M: the exact wedge this fixes.)
	lower, clamped, unrecoverable := applyPruneFloor(17_400_000, 18_300_000, 20_395_001)
	c.Check(unrecoverable, Equals, true)
	c.Check(clamped, Equals, false)
	c.Check(lower, Equals, int64(17_400_000))

	// Range straddles the floor → clamp lower to earliest-1, still scannable —
	// but the pruned part of the window is gone, so a subsequent scan miss must
	// be classified unrecoverable (retrying only loses more history), not as a
	// transient not-found that wedges the foreground retry loop.
	lower, clamped, unrecoverable = applyPruneFloor(500, 800, 601)
	c.Check(unrecoverable, Equals, false)
	c.Check(clamped, Equals, true)
	c.Check(lower, Equals, int64(600))

	// Range fully above the floor → untouched.
	lower, clamped, unrecoverable = applyPruneFloor(700, 900, 601)
	c.Check(unrecoverable, Equals, false)
	c.Check(clamped, Equals, false)
	c.Check(lower, Equals, int64(700))

	// Floor unknown (earliest <= 0) → disabled, no change, never unrecoverable.
	lower, clamped, unrecoverable = applyPruneFloor(500, 800, 0)
	c.Check(unrecoverable, Equals, false)
	c.Check(clamped, Equals, false)
	c.Check(lower, Equals, int64(500))
}

func (s *RecoverKeysharesSuite) TestScanMissSentinelDependsOnClamping(c *C) {
	// Full window scanned, nothing found → possibly transient (flaky RPC collapsed
	// the window, message just outside) → retryable.
	c.Check(errors.Is(scanMissSentinel(false), errRecoveryBackupNotFound), Equals, true)
	// Part of the window was pruned away → the message may sit in the lost part and
	// retrying can only lose more history → definitively unrecoverable from this
	// node (degraded start + background retry, where repointing at an archive
	// endpoint widens the window again).
	c.Check(errors.Is(scanMissSentinel(true), errRecoveryBackupUnrecoverable), Equals, true)
	c.Check(errors.Is(scanMissSentinel(true), errRecoveryBackupNotFound), Equals, false)
}

func (s *RecoverKeysharesSuite) TestShouldSkipUnrecoverable(c *C) {
	unrec := fmt.Errorf("wrap: %w", errRecoveryBackupUnrecoverable)
	notFound := fmt.Errorf("wrap: %w", errRecoveryBackupNotFound)

	// Definitively unrecoverable + non-signing (inactive) vault → skip (continue
	// startup so bifrost reaches TSS instead of wedging on a lost share nobody
	// signs with any more).
	c.Check(shouldSkipUnrecoverable(unrec, false), Equals, true)
	// Same error on a SIGNING (active/retiring) vault → never skip silently: the
	// error propagates so main.go starts bifrost degraded and keeps retrying
	// recovery in the background, screaming on every cycle.
	c.Check(shouldSkipUnrecoverable(unrec, true), Equals, false)
	// Retryable not-found (e.g. a flaky RPC collapsed the scan window) → never skip;
	// it stays fatal so the startup loop can retry and succeed once RPC recovers.
	c.Check(shouldSkipUnrecoverable(notFound, false), Equals, false)
	// Arbitrary/transient error → never skip.
	c.Check(shouldSkipUnrecoverable(fmt.Errorf("transient rpc error"), false), Equals, false)
	// nil → never skip.
	c.Check(shouldSkipUnrecoverable(nil, false), Equals, false)

	// recoverLegacyKeySharesFromBackups joins the ecdsa and eddsa outcomes. A mixed
	// join — ecdsa found-but-empty (unrecoverable) alongside a TRANSIENT eddsa write
	// failure — must not be skipped: skipping would swallow the transient failure
	// that a single retry would fix, and the node would drop out of every eddsa
	// keysign for that vault.
	mixed := errors.Join(unrec, fmt.Errorf("failed to recover eddsa key shares: %w", errors.New("disk full")))
	c.Check(shouldSkipUnrecoverable(mixed, false), Equals, false)
	// Every member unrecoverable → skippable (for a non-signing vault).
	allUnrec := errors.Join(unrec, fmt.Errorf("wrap: %w", errRecoveryBackupUnrecoverable))
	c.Check(shouldSkipUnrecoverable(allUnrec, false), Equals, true)
}

func (s *RecoverKeysharesSuite) TestIsSigningVault(c *C) {
	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{pubKey: pk}
	c.Check(isSigningVault(target, types.Vaults{{PubKey: pk, Status: types.VaultStatus_ActiveVault}}), Equals, true)
	// A retiring vault still signs (migration outbounds — see the RetiringVault
	// semaphore in bifrost/signer/pipeline.go), so it gets the same never-skip
	// protection as an active one.
	c.Check(isSigningVault(target, types.Vaults{{PubKey: pk, Status: types.VaultStatus_RetiringVault}}), Equals, true)
	c.Check(isSigningVault(target, types.Vaults{{PubKey: pk, Status: types.VaultStatus_InactiveVault}}), Equals, false)
	// Vault not present in the set → conservative default: treat as signing so the
	// skip path never fires on a vault whose status we could not determine.
	c.Check(isSigningVault(target, types.Vaults{}), Equals, true)
}

func (s *RecoverKeysharesSuite) TestIsUnrecoverableKeyshareError(c *C) {
	c.Check(IsUnrecoverableKeyshareError(errRecoveryBackupUnrecoverable), Equals, true)
	c.Check(IsUnrecoverableKeyshareError(fmt.Errorf("wrap: %w", errRecoveryBackupUnrecoverable)), Equals, true)
	// recoverKeyShares joins per-target errors; one unrecoverable member is enough
	// for main.go to start bifrost degraded and hand recovery to the background
	// supervisor (which retries every joined target anyway).
	c.Check(IsUnrecoverableKeyshareError(errors.Join(fmt.Errorf("transient"), fmt.Errorf("wrap: %w", errRecoveryBackupUnrecoverable))), Equals, true)
	c.Check(IsUnrecoverableKeyshareError(fmt.Errorf("wrap: %w", errRecoveryBackupNotFound)), Equals, false)
	c.Check(IsUnrecoverableKeyshareError(fmt.Errorf("transient rpc error")), Equals, false)
	c.Check(IsUnrecoverableKeyshareError(nil), Equals, false)
}

func (s *RecoverKeysharesSuite) TestFrostRecoveryBackupMissingIsSkippable(c *C) {
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, fmt.Errorf("wrapped: %w", errRecoveryBackupNotFound)), Equals, true)
	// Definitively unrecoverable (pruned below the node floor / found-but-empty) is
	// just as absent as not-found from the FROST supervisor's perspective — without
	// this the background FROST retry loop would hammer and error-log forever on a
	// vault whose TssPool message is below the prune floor.
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, fmt.Errorf("wrapped: %w", errRecoveryBackupUnrecoverable)), Equals, true)
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, nil), Equals, true)
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{frost: []byte("backup")}, nil), Equals, false)
	c.Check(isFrostRecoveryBackupMissing(recoveryBackups{}, fmt.Errorf("decode failed")), Equals, false)
}

func (s *RecoverKeysharesSuite) TestFindKeyshareRecoveryTargetPrefersFrostPubKey(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	frostPubKey := types.GetRandomEd25519PubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	vaults := types.Vaults{
		{
			PubKey:      legacyPubKey,
			PubKeyEddsa: eddsaPubKey,
			PubKeyFrost: frostPubKey,
			BlockHeight: 123,
		},
	}

	target, ok := findKeyshareRecoveryTarget(frostPubKey, vaults)
	c.Assert(ok, Equals, true)
	c.Check(target.kind, Equals, recoveryTargetFrost)
	c.Check(target.pubKey.Equals(frostPubKey), Equals, true)
	c.Check(target.blockHeight, Equals, int64(123))

	target, ok = findKeyshareRecoveryTarget(legacyPubKey, vaults)
	c.Assert(ok, Equals, true)
	c.Check(target.kind, Equals, recoveryTargetLegacy)
	c.Check(target.pubKey.Equals(legacyPubKey), Equals, true)
	c.Check(target.poolPubKeyEddsa.Equals(eddsaPubKey), Equals, true)

	_, ok = findKeyshareRecoveryTarget(types.GetRandomPubKey(), vaults)
	c.Check(ok, Equals, false)
}

func (s *RecoverKeysharesSuite) TestAugmentVaultsWithMissingMembershipFetchesMissing(c *C) {
	active := types.Vault{PubKey: types.GetRandomPubKey(), BlockHeight: 100}
	inactive := types.Vault{PubKey: types.GetRandomPubKey(), BlockHeight: 50}
	membership := common.PubKeys{active.PubKey, inactive.PubKey}
	vaults := types.Vaults{active}

	fetched := common.PubKeys{}
	result := augmentVaultsWithMissingMembership(membership, vaults, func(pk common.PubKey) (types.Vault, error) {
		fetched = append(fetched, pk)
		return inactive, nil
	})

	c.Assert(fetched, HasLen, 1)
	c.Check(fetched[0].Equals(inactive.PubKey), Equals, true, Commentf("only the missing vault should be fetched"))
	c.Assert(result, HasLen, 2)
	c.Check(result[1].PubKey.Equals(inactive.PubKey), Equals, true)
}

func (s *RecoverKeysharesSuite) TestAugmentVaultsWithMissingMembershipSkipsFetchFailure(c *C) {
	missing := types.GetRandomPubKey()
	membership := common.PubKeys{missing}

	result := augmentVaultsWithMissingMembership(membership, types.Vaults{}, func(pk common.PubKey) (types.Vault, error) {
		return types.Vault{}, fmt.Errorf("thorchain unreachable")
	})

	// Fetch failure is skipped; keyshareRecoveryTargets still surfaces the
	// missing entry so the outer retry loop keeps working.
	c.Assert(result, HasLen, 0)
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsResolvesEveryMembership(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	frostPubKey := types.GetRandomEd25519PubKey()

	// A node that churned through Monero support is a member of both a legacy
	// asgard vault and a separate XMR monovault (PubKey == PubKeyFrost).
	vaults := types.Vaults{
		{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 100},
		{PubKey: frostPubKey, PubKeyFrost: frostPubKey, BlockHeight: 200},
	}
	membership := common.PubKeys{legacyPubKey, frostPubKey}

	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, nil)
	c.Assert(recoveryErrs, HasLen, 0)
	c.Assert(targets, HasLen, 2)
	c.Check(targets[0].kind, Equals, recoveryTargetLegacy)
	c.Check(targets[0].pubKey.Equals(legacyPubKey), Equals, true)
	c.Check(targets[0].poolPubKeyEddsa.Equals(eddsaPubKey), Equals, true)
	c.Check(targets[1].kind, Equals, recoveryTargetFrost)
	c.Check(targets[1].pubKey.Equals(frostPubKey), Equals, true)
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsFiltersRecoveryScope(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	frostPubKey := types.GetRandomEd25519PubKey()
	unknownPubKey := types.GetRandomPubKey()
	vaults := types.Vaults{
		{PubKey: legacyPubKey, BlockHeight: 100},
		{PubKey: frostPubKey, PubKeyFrost: frostPubKey, BlockHeight: 200},
	}
	membership := common.PubKeys{unknownPubKey, legacyPubKey, frostPubKey}

	frostKind := recoveryTargetFrost
	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, &frostKind)
	c.Assert(recoveryErrs, HasLen, 0)
	c.Assert(targets, HasLen, 1)
	c.Check(targets[0].kind, Equals, recoveryTargetFrost)

	legacyKind := recoveryTargetLegacy
	targets, recoveryErrs = keyshareRecoveryTargets(membership, vaults, &legacyKind)
	c.Assert(recoveryErrs, HasLen, 1)
	c.Assert(targets, HasLen, 1)
	c.Check(targets[0].kind, Equals, recoveryTargetLegacy)
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsRetainsOlderVaults(c *C) {
	oldPubKey := types.GetRandomPubKey()
	newPubKey := types.GetRandomPubKey()
	vaults := types.Vaults{
		{PubKey: oldPubKey, BlockHeight: 100},
		{PubKey: newPubKey, BlockHeight: 300},
	}
	// The older vault remains a recovery target because it may still need to
	// migrate funds or issue refunds while retiring.
	membership := common.PubKeys{newPubKey, oldPubKey}

	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, nil)
	c.Assert(recoveryErrs, HasLen, 0)
	c.Assert(targets, HasLen, 2)
	c.Check(targets[0].kind, Equals, recoveryTargetLegacy)
	c.Check(targets[0].pubKey.Equals(newPubKey), Equals, true)
	c.Check(targets[0].blockHeight, Equals, int64(300))
	c.Check(targets[1].pubKey.Equals(oldPubKey), Equals, true)
	c.Check(targets[1].blockHeight, Equals, int64(100))
}

func (s *RecoverKeysharesSuite) TestKeyshareRecoveryTargetsReportsUnknownMembership(c *C) {
	legacyPubKey := types.GetRandomPubKey()
	vaults := types.Vaults{{PubKey: legacyPubKey, BlockHeight: 100}}
	membership := common.PubKeys{types.GetRandomPubKey(), legacyPubKey}

	targets, recoveryErrs := keyshareRecoveryTargets(membership, vaults, nil)
	c.Assert(targets, HasLen, 1)
	c.Assert(recoveryErrs, HasLen, 1)
	c.Check(targets[0].pubKey.Equals(legacyPubKey), Equals, true)

	// No membership entry matches a known vault.
	targets, recoveryErrs = keyshareRecoveryTargets(common.PubKeys{types.GetRandomPubKey()}, vaults, nil)
	c.Assert(targets, HasLen, 0)
	c.Assert(recoveryErrs, HasLen, 1)
}

func (s *RecoverKeysharesSuite) TestMsgMatchesRecoveryTargetRequiresExactKeyType(c *C) {
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	otherSigner, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	legacyPubKey := types.GetRandomPubKey()
	frostPubKey := types.GetRandomEd25519PubKey()

	xmrMsg := &types.MsgTssPool{
		Signer:          signer,
		KeygenType:      types.KeygenType_XMRMonovaultKeygen,
		PoolPubKey:      legacyPubKey,
		PoolPubKeyFrost: frostPubKey,
	}
	frostTarget := keyshareRecoveryTarget{kind: recoveryTargetFrost, pubKey: frostPubKey}
	legacyTarget := keyshareRecoveryTarget{kind: recoveryTargetLegacy, pubKey: legacyPubKey}

	c.Check(msgMatchesRecoveryTarget(xmrMsg, signer, frostTarget), Equals, true)
	c.Check(msgMatchesRecoveryTarget(xmrMsg, signer, legacyTarget), Equals, true)
	c.Check(msgMatchesRecoveryTarget(xmrMsg, otherSigner, frostTarget), Equals, false)
	c.Check(msgMatchesRecoveryTarget(xmrMsg, signer, keyshareRecoveryTarget{kind: recoveryTargetFrost, pubKey: common.EmptyPubKey}), Equals, false)

	legacyMsg := &types.MsgTssPool{
		Signer:     signer,
		KeygenType: types.KeygenType_AsgardKeygen,
		PoolPubKey: legacyPubKey,
	}
	c.Check(msgMatchesRecoveryTarget(legacyMsg, signer, legacyTarget), Equals, true)
	c.Check(msgMatchesRecoveryTarget(legacyMsg, signer, frostTarget), Equals, false)
}

func (s *RecoverKeysharesSuite) TestRecoverySeedPhraseUsesSharedSignerSeed(c *C) {
	originalSeedPhrase, hadOriginalSeedPhrase := os.LookupEnv("SIGNER_SEED_PHRASE")
	originalSeedPhraseFile, hadOriginalSeedPhraseFile := os.LookupEnv("SIGNER_SEED_PHRASE_FILE")
	seedPhraseFile := filepath.Join(c.MkDir(), "seed-phrase")
	defer func() {
		if hadOriginalSeedPhrase {
			_ = os.Setenv("SIGNER_SEED_PHRASE", originalSeedPhrase)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE")
		}
		if hadOriginalSeedPhraseFile {
			_ = os.Setenv("SIGNER_SEED_PHRASE_FILE", originalSeedPhraseFile)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE_FILE")
		}
	}()

	_ = os.Unsetenv("SIGNER_SEED_PHRASE")
	_ = os.Unsetenv("SIGNER_SEED_PHRASE_FILE")
	seedPhrase, err := recoverySeedPhrase()
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "failed to get signer seed phrase: could not load signer seed phrase: SIGNER_SEED_PHRASE_FILE is unset or invalid (SIGNER_SEED_PHRASE env var is only honored on mocknet)")
	c.Assert(seedPhrase, Equals, "")

	c.Assert(os.WriteFile(seedPhraseFile, []byte("  "+Mnemonic+"  "), 0o600), IsNil)
	err = os.Setenv("SIGNER_SEED_PHRASE_FILE", seedPhraseFile)
	c.Assert(err, IsNil)
	seedPhrase, err = recoverySeedPhrase()
	c.Assert(err, IsNil)
	c.Assert(seedPhrase, Equals, Mnemonic)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesRecoversMissingEddsaWhenECDSAExists(c *C) {
	dir, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	original := readLocalStateTestFile(c)
	c.Assert(os.WriteFile(localstateKeysharePath(legacyPubKey), original, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{
		eddsa: encryptedLocalStateTestFile(c),
	})
	c.Assert(err, IsNil)
	assertRecoveredLocalState(c, filepath.Join(dir, "localstate-"+eddsaPubKey.String()+".json"), original)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesRecoversMissingECDSAWhenEddsaExists(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	original := readLocalStateTestFile(c)
	c.Assert(os.WriteFile(localstateKeysharePath(eddsaPubKey), original, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{
		secp256k1: encryptedLocalStateTestFile(c),
	})
	c.Assert(err, IsNil)
	assertRecoveredLocalState(c, localstateKeysharePath(legacyPubKey), original)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesBothExistingSkipsBackupsAndSeed(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE"), IsNil)

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	original := readLocalStateTestFile(c)
	c.Assert(os.WriteFile(localstateKeysharePath(legacyPubKey), original, 0o600), IsNil)
	c.Assert(os.WriteFile(localstateKeysharePath(eddsaPubKey), original, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{})
	c.Assert(err, IsNil)
}

func (s *RecoverKeysharesSuite) TestRecoverLegacyKeySharesMissingEddsaBackupReturnsError(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	c.Assert(os.WriteFile(localstateKeysharePath(legacyPubKey), readLocalStateTestFile(c), 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{})
	c.Assert(err, NotNil)
	// A found-but-empty backup is now classified as definitively unrecoverable, so a
	// non-active vault can be skipped (continue startup) instead of wedging on it.
	c.Assert(errors.Is(err, errRecoveryBackupUnrecoverable), Equals, true)
	c.Assert(err.Error(), Matches, ".*eddsa key shares backup empty for "+eddsaPubKey.String())
	_, statErr := os.Stat(localstateKeysharePath(eddsaPubKey))
	c.Assert(os.IsNotExist(statErr), Equals, true)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredDecompressedKeysharesWritesOwnerOnlyFile(c *C) {
	encrypted, err := EncryptKeyshares(LocalStateTestFile, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "localstate-recovered.json")
	err = writeRecoveredDecompressedKeyshares(path, encrypted, Mnemonic)
	c.Assert(err, IsNil)

	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))

	original, err := os.ReadFile(LocalStateTestFile)
	c.Assert(err, IsNil)
	recoveredRaw, err := os.ReadFile(path)
	c.Assert(err, IsNil)

	// recovered file should be at-rest encrypted
	c.Assert(storage.IsEncryptedAtRest(recoveredRaw), Equals, true)
	atRestKey := storage.DeriveAtRestKey(Mnemonic)
	recovered, err := storage.DecryptAtRest(atRestKey, recoveredRaw)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, original)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredDecompressedKeysharesDoesNotLeaveOrphanOnDecryptFailure(c *C) {
	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "localstate-recovered.json")
	err = writeRecoveredDecompressedKeyshares(path, []byte("broken"), Mnemonic)
	c.Assert(err, NotNil)

	_, err = os.Stat(path)
	c.Assert(os.IsNotExist(err), Equals, true)

	entries, err := os.ReadDir(dir)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 0)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredRawKeysharesWritesEncryptedOwnerOnlyFile(c *C) {
	rawKeyshares := []byte{0x07, 0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}
	vaultPubKey := types.GetRandomEd25519PubKey()
	encrypted, err := EncryptRawKeyshares(rawKeyshares, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "frost-recovery.hex")
	err = writeRecoveredRawKeyshares(path, encrypted, Mnemonic, "mocknet", vaultPubKey)
	c.Assert(err, IsNil)

	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))

	recoveredRaw, err := os.ReadFile(path)
	c.Assert(err, IsNil)

	// recovered file should be at-rest encrypted
	c.Assert(storage.IsEncryptedAtRest(recoveredRaw), Equals, true)
	atRestKey := storage.DeriveAtRestKey(Mnemonic)
	recoveredEnvelope, err := storage.DecryptAtRest(atRestKey, recoveredRaw)
	c.Assert(err, IsNil)
	recovered, err := decodeFrostRecoveryEnvelope(recoveredEnvelope, "mocknet", vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, rawKeyshares)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", vaultPubKey), IsNil)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "chainnet", vaultPubKey), NotNil)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", types.GetRandomEd25519PubKey()), NotNil)
}

func (s *RecoverKeysharesSuite) TestFrostRecoveryEnvelopeRejectsLegacyAndTampering(c *C) {
	vaultPubKey := types.GetRandomEd25519PubKey()
	_, err := decodeFrostRecoveryEnvelope([]byte("legacy-raw-keyshare"), "mocknet", vaultPubKey)
	c.Assert(err, NotNil)

	envelope, err := encodeFrostRecoveryEnvelope([]byte("secret-keyshare"), "mocknet", vaultPubKey)
	c.Assert(err, IsNil)
	recovered, err := decodeFrostRecoveryEnvelope(envelope, "mocknet", vaultPubKey)
	c.Assert(err, IsNil)
	c.Assert(recovered, DeepEquals, []byte("secret-keyshare"))

	tampered := append([]byte(nil), envelope...)
	tampered[len(tampered)-1] ^= 1
	_, err = decodeFrostRecoveryEnvelope(tampered, "mocknet", vaultPubKey)
	c.Assert(err, NotNil)
}

func (s *RecoverKeysharesSuite) TestWriteRecoveredBytesDoesNotReplaceExisting(c *C) {
	dir, err := os.MkdirTemp("", "recover-keyshares-publish-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "frost-recovery-test.hex")
	want := []byte("existing-authenticated-envelope")
	c.Assert(os.WriteFile(path, want, 0o600), IsNil)
	c.Assert(writeRecoveredBytes(path, []byte("replacement")), NotNil)
	got, err := os.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(got, DeepEquals, want)
	entries, err := os.ReadDir(dir)
	c.Assert(err, IsNil)
	c.Assert(entries, HasLen, 1)
}

func (s *RecoverKeysharesSuite) TestLegacyRecoveryReplacesEmptyArtifacts(c *C) {
	_, cleanup := setupRecoveryKeysharesTest(c)
	defer cleanup()

	legacyPubKey := types.GetRandomPubKey()
	eddsaPubKey := types.GetRandomEd25519PubKey()
	signer, err := types.GetRandomPubKey().GetThorAddress()
	c.Assert(err, IsNil)
	target := keyshareRecoveryTarget{
		kind:            recoveryTargetLegacy,
		pubKey:          legacyPubKey,
		poolPubKeyEddsa: eddsaPubKey,
	}
	vaults := types.Vaults{{PubKey: legacyPubKey, PubKeyEddsa: eddsaPubKey, BlockHeight: 123}}
	ecdsaPath := localstateKeysharePath(legacyPubKey)
	eddsaPath := localstateKeysharePath(eddsaPubKey)
	c.Assert(os.WriteFile(ecdsaPath, nil, 0o600), IsNil)
	c.Assert(os.WriteFile(eddsaPath, nil, 0o600), IsNil)

	err = recoverLegacyKeySharesFromBackups(signer, target, vaults, recoveryBackups{
		secp256k1: encryptedLocalStateTestFile(c),
		eddsa:     encryptedLocalStateTestFile(c),
	})
	c.Assert(err, IsNil)
	original := readLocalStateTestFile(c)
	assertRecoveredLocalState(c, ecdsaPath, original)
	assertRecoveredLocalState(c, eddsaPath, original)
	assertEmptyRecoveryArtifactQuarantined(c, ecdsaPath)
	assertEmptyRecoveryArtifactQuarantined(c, eddsaPath)
}

func (s *RecoverKeysharesSuite) TestFrostRecoveryReplacesEmptyArtifact(c *C) {
	rawKeyshares := []byte("deadbeefcafebabe")
	vaultPubKey := types.GetRandomEd25519PubKey()
	encrypted, err := EncryptRawKeyshares(rawKeyshares, Mnemonic)
	c.Assert(err, IsNil)

	dir, err := os.MkdirTemp("", "recover-keyshares-empty-frost-*")
	c.Assert(err, IsNil)
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "frost-recovery.hex")
	c.Assert(os.WriteFile(path, nil, 0o600), IsNil)
	exists, err := recoveryFileExists(path)
	c.Assert(err, IsNil)
	c.Assert(exists, Equals, false)
	c.Assert(writeRecoveredRawKeyshares(path, encrypted, Mnemonic, "mocknet", vaultPubKey), IsNil)
	c.Assert(validateFrostRecoveryFile(path, Mnemonic, "mocknet", vaultPubKey), IsNil)
	assertEmptyRecoveryArtifactQuarantined(c, path)
}

func assertEmptyRecoveryArtifactQuarantined(c *C, path string) {
	matches, err := filepath.Glob(path + ".invalid-*")
	c.Assert(err, IsNil)
	c.Assert(matches, HasLen, 1)
	fi, err := os.Stat(matches[0])
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().IsRegular(), Equals, true)
	c.Assert(fi.Size(), Equals, int64(0))
}

func setupRecoveryKeysharesTest(c *C) (string, func()) {
	dir, err := os.MkdirTemp("", "recover-keyshares-home-*")
	c.Assert(err, IsNil)

	originalHome := keyshareRecoveryNodeHome
	originalSeedPhrase, hadOriginalSeedPhrase := os.LookupEnv("SIGNER_SEED_PHRASE")
	originalSeedPhraseFile, hadOriginalSeedPhraseFile := os.LookupEnv("SIGNER_SEED_PHRASE_FILE")

	keyshareRecoveryNodeHome = dir
	c.Assert(os.Unsetenv("SIGNER_SEED_PHRASE"), IsNil)
	c.Assert(os.Setenv("SIGNER_SEED_PHRASE_FILE", writeRecoverySeedPhraseFile(c, dir, Mnemonic)), IsNil)

	return dir, func() {
		keyshareRecoveryNodeHome = originalHome
		if hadOriginalSeedPhrase {
			_ = os.Setenv("SIGNER_SEED_PHRASE", originalSeedPhrase)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE")
		}
		if hadOriginalSeedPhraseFile {
			_ = os.Setenv("SIGNER_SEED_PHRASE_FILE", originalSeedPhraseFile)
		} else {
			_ = os.Unsetenv("SIGNER_SEED_PHRASE_FILE")
		}
		_ = os.RemoveAll(dir)
	}
}

func writeRecoverySeedPhraseFile(c *C, dir, seedPhrase string) string {
	path := filepath.Join(dir, "signer-seed-phrase")
	c.Assert(os.WriteFile(path, []byte(seedPhrase), 0o600), IsNil)
	return path
}

func encryptedLocalStateTestFile(c *C) []byte {
	encrypted, err := EncryptKeyshares(LocalStateTestFile, Mnemonic)
	c.Assert(err, IsNil)
	return encrypted
}

func readLocalStateTestFile(c *C) []byte {
	original, err := os.ReadFile(LocalStateTestFile)
	c.Assert(err, IsNil)
	return original
}

func assertRecoveredLocalState(c *C, path string, original []byte) {
	recovered, err := os.ReadFile(path)
	c.Assert(err, IsNil)
	c.Assert(storage.IsEncryptedAtRest(recovered), Equals, true)
	atRestKey := storage.DeriveAtRestKey(Mnemonic)
	decrypted, err := storage.DecryptAtRest(atRestKey, recovered)
	c.Assert(err, IsNil)
	c.Assert(decrypted, DeepEquals, original)
	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode().Perm(), Equals, os.FileMode(0o600))
}

func (s *RecoverKeysharesSuite) TestBlockPrunedMidScan(c *C) {
	// The floor is sampled once before a scan that can run for thousands of
	// blocks. Pruning advances during that scan, and an LB may answer /status
	// from a different instance than the one serving Block() — so a fetch
	// failure must be re-checked against a FRESH floor before it is trusted.

	// Height fell below the fresh floor → pruned out from under the scan.
	c.Check(blockPrunedMidScan(600, 700, nil), Equals, true)
	// Height still above the fresh floor → a genuine RPC failure, stays transient.
	c.Check(blockPrunedMidScan(800, 700, nil), Equals, false)
	// Exactly at the floor is still fetchable.
	c.Check(blockPrunedMidScan(700, 700, nil), Equals, false)
	// Fresh floor unavailable → cannot claim pruning; stay transient/retryable.
	c.Check(blockPrunedMidScan(600, 700, errors.New("status rpc failed")), Equals, false)
	c.Check(blockPrunedMidScan(600, 0, nil), Equals, false)
}

func (s *RecoverKeysharesSuite) TestSkipDecisionAcrossVaultStatusAndErrorClass(c *C) {
	pk := types.GetRandomPubKey()
	target := keyshareRecoveryTarget{pubKey: pk}

	statuses := []struct {
		name   string
		vaults types.Vaults
	}{
		{"active", types.Vaults{{PubKey: pk, Status: types.VaultStatus_ActiveVault}}},
		{"retiring", types.Vaults{{PubKey: pk, Status: types.VaultStatus_RetiringVault}}},
		{"inactive", types.Vaults{{PubKey: pk, Status: types.VaultStatus_InactiveVault}}},
		{"absent", types.Vaults{}},
	}
	errClasses := []struct {
		name string
		err  error
	}{
		{"pruned", fmt.Errorf("%w: below the earliest available block", errRecoveryBackupUnrecoverable)},
		{"empty", fmt.Errorf("%w: ecdsa key shares backup empty", errRecoveryBackupUnrecoverable)},
		{"not-found", fmt.Errorf("%w: TssPool message not found", errRecoveryBackupNotFound)},
		{"transient", errors.New("fail to get block: connection reset")},
	}

	// Skipping is safe ONLY where the vault will never be asked to sign again
	// (inactive) AND the failure is definitively unrecoverable (pruned/empty).
	// Everything else must surface: signing vaults degrade + retry in the
	// background, and retryable classes stay retryable. "absent" is conservative
	// — an unknown status is treated as signing.
	want := map[string]bool{
		"inactive/pruned": true,
		"inactive/empty":  true,
	}
	for _, st := range statuses {
		for _, ec := range errClasses {
			key := st.name + "/" + ec.name
			got := shouldSkipUnrecoverable(ec.err, isSigningVault(target, st.vaults))
			c.Check(got, Equals, want[key], Commentf("case %s", key))
		}
	}
}
