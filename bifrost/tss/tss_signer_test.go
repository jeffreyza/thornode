package tss

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tctypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type KeySignTestSuite struct {
	batchInterval time.Duration
}

var _ = Suite(&KeySignTestSuite{})

// The mock TSS server answers at once; keep the keysign batching but skip its
// real 1s wait for a quiet queue. Every test stops its KeySign, so no batch
// loop is running when the suite restores the interval.
func (s *KeySignTestSuite) SetUpSuite(c *C) {
	s.batchInterval = KeySignBatchInterval
	KeySignBatchInterval = 10 * time.Millisecond
}

func (s *KeySignTestSuite) TearDownSuite(c *C) {
	KeySignBatchInterval = s.batchInterval
}

// ---------------------------------------------------------------------------
// Mock tssServer
// ---------------------------------------------------------------------------

type mockTssServer struct {
	resp    keysign.Response
	err     error
	lastReq keysign.Request
}

func (m *mockTssServer) KeySign(req keysign.Request) (keysign.Response, error) {
	m.lastReq = req
	return m.resp, m.err
}

// ---------------------------------------------------------------------------
// Mock ThorchainBridge (full interface)
// ---------------------------------------------------------------------------

type mockBridgeForKeysign struct {
	version               semver.Version
	versionErr            error
	blockHeight           int64
	blockErr              error
	keysignParty          common.PubKeys
	neverSigners          common.PubKeys
	jailedFallbackSigners common.PubKeys
	requiredSigners       int
	keysignPartyErr       error
	keysignPartyCalls     int
	lastKeysignPartyKey   common.PubKey
	lastKeysignPartyBlock int64
}

var _ thorclient.ThorchainBridge = (*mockBridgeForKeysign)(nil)

func (m *mockBridgeForKeysign) GetOraclePrice(string) (cosmos.Uint, error) {
	return cosmos.ZeroUint(), errors.New("oracle price unavailable")
}

func (m *mockBridgeForKeysign) EnsureNodeWhitelisted() error                { return nil }
func (m *mockBridgeForKeysign) EnsureNodeWhitelistedWithTimeout() error     { return nil }
func (m *mockBridgeForKeysign) FetchNodeStatus() (stypes.NodeStatus, error) { return 0, nil }
func (m *mockBridgeForKeysign) FetchActiveNodes() ([]common.PubKey, error)  { return nil, nil }

// GetKeysignPolicy records the requested vault and policy height while
// returning the injected policy fields used by keysign request tests.
func (m *mockBridgeForKeysign) GetKeysignPolicy(pubKey common.PubKey, blockHeight int64) (thorclient.KeysignPolicy, error) {
	m.keysignPartyCalls++
	m.lastKeysignPartyKey = pubKey
	m.lastKeysignPartyBlock = blockHeight
	return thorclient.KeysignPolicy{
		PubKey:                pubKey,
		Signers:               m.keysignParty,
		NeverSigners:          m.neverSigners,
		JailedFallbackSigners: m.jailedFallbackSigners,
		RequiredSigners:       m.requiredSigners,
		ActiveSignerCount:     len(m.keysignParty),
	}, m.keysignPartyErr
}

// GetKeysignParty preserves the legacy signer-only bridge helper on top of the
// full policy mock.
func (m *mockBridgeForKeysign) GetKeysignParty(pubKey common.PubKey, blockHeight int64) (common.PubKeys, error) {
	policy, err := m.GetKeysignPolicy(pubKey, blockHeight)
	return policy.Signers, err
}
func (m *mockBridgeForKeysign) GetAsgards() (stypes.Vaults, error) { return nil, nil }
func (m *mockBridgeForKeysign) GetVault(string) (stypes.Vault, error) {
	return stypes.Vault{}, nil
}

func (m *mockBridgeForKeysign) GetConfig() config.BifrostClientConfiguration {
	return config.BifrostClientConfiguration{}
}
func (m *mockBridgeForKeysign) GetConstants() (map[string]int64, error) { return nil, nil }
func (m *mockBridgeForKeysign) GetContext() client.Context              { return client.Context{} }
func (m *mockBridgeForKeysign) GetContractAddress() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, nil
}
func (m *mockBridgeForKeysign) GetErrataMsg(common.TxID, common.Chain) sdk.Msg { return nil }
func (m *mockBridgeForKeysign) GetKeygenStdTx(common.PubKey, []byte, []byte, []stypes.Blame, common.PubKeys, stypes.KeygenType, common.Chains, int64, int64, common.PubKey, []byte, ...any) (sdk.Msg, error) {
	return nil, nil
}

func (m *mockBridgeForKeysign) GetMimir(string) (int64, error)                { return 0, nil }
func (m *mockBridgeForKeysign) GetMimirWithRef(string, string) (int64, error) { return 0, nil }
func (m *mockBridgeForKeysign) GetInboundOutbound(common.ObservedTxs) (common.ObservedTxs, common.ObservedTxs, error) {
	return nil, nil, nil
}
func (m *mockBridgeForKeysign) GetPools() (stypes.Pools, error) { return nil, nil }
func (m *mockBridgeForKeysign) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, nil
}

func (m *mockBridgeForKeysign) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, nil
}

func (m *mockBridgeForKeysign) GetSolvencyMsg(int64, common.Chain, common.PubKey, common.Coins) *stypes.MsgSolvency {
	return nil
}

func (m *mockBridgeForKeysign) GetTHORName(string) (stypes.THORName, error) {
	return stypes.THORName{}, nil
}

func (m *mockBridgeForKeysign) GetThorchainVersion() (semver.Version, error) {
	return m.version, m.versionErr
}
func (m *mockBridgeForKeysign) IsCatchingUp() (bool, error)              { return false, nil }
func (m *mockBridgeForKeysign) HasNetworkFee(common.Chain) (bool, error) { return false, nil }
func (m *mockBridgeForKeysign) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 0, 0, nil
}

func (m *mockBridgeForKeysign) PostKeysignFailure(stypes.Blame, int64, string, common.Coins, common.PubKey) (common.TxID, error) {
	return "", nil
}

func (m *mockBridgeForKeysign) PostNetworkFee(int64, common.Chain, uint64, uint64) (common.TxID, error) {
	return "", nil
}
func (m *mockBridgeForKeysign) RagnarokInProgress() (bool, error) { return false, nil }
func (m *mockBridgeForKeysign) GetObservedTxVoter(string) (*stypes.QueryObservedTxVoter, error) {
	return nil, nil
}

func (m *mockBridgeForKeysign) GetObservedTxOutVoter(txID string) (*stypes.QueryObservedTxVoter, error) {
	return m.GetObservedTxVoter(txID)
}

func (m *mockBridgeForKeysign) GetObservedTxOutVoterUncached(ctx context.Context, txID string) (*stypes.QueryObservedTxVoter, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	voter, err := m.GetObservedTxVoter(txID)
	return voter, voter != nil, err
}

func (m *mockBridgeForKeysign) GetXMRSpentOutputRefs(common.PubKey) ([]*common.OutputRef, error) {
	return nil, nil
}

func (m *mockBridgeForKeysign) GetXMRSpentOutputRefsUncached(ctx context.Context, vaultPubKey common.PubKey) ([]*common.OutputRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return m.GetXMRSpentOutputRefs(vaultPubKey)
}

func (m *mockBridgeForKeysign) WaitToCatchUp() error { return nil }
func (m *mockBridgeForKeysign) GetBlockHeight() (int64, error) {
	return m.blockHeight, m.blockErr
}

func (m *mockBridgeForKeysign) GetBlockTimestamp(int64) (time.Time, error) {
	return time.Time{}, nil
}

func (m *mockBridgeForKeysign) GetLastObservedInHeight(common.Chain) (int64, error) { return 0, nil }

func (m *mockBridgeForKeysign) GetLastObservedInHeightAtHeight(common.Chain, int64) (int64, error) {
	return 0, nil
}

func (m *mockBridgeForKeysign) GetLastSignedOutHeight(common.Chain) (int64, error) { return 0, nil }

func (m *mockBridgeForKeysign) Broadcast(...sdk.Msg) (common.TxID, error) { return "", nil }

func (m *mockBridgeForKeysign) BroadcastWithBlocking(...sdk.Msg) (common.TxID, error) {
	return "", nil
}

func (m *mockBridgeForKeysign) GetKeysign(int64, string) (tctypes.TxOut, error) {
	return tctypes.TxOut{}, nil
}

func (m *mockBridgeForKeysign) GetNodeAccount(string) (*stypes.NodeAccount, error) { return nil, nil }

func (m *mockBridgeForKeysign) GetNodeAccounts() ([]*stypes.QueryNodeResponse, error) {
	return nil, nil
}

func (m *mockBridgeForKeysign) GetKeygenBlock(int64, string) (stypes.KeygenBlock, error) {
	return stypes.KeygenBlock{}, nil
}

func (m *mockBridgeForKeysign) GetReferenceMemo(common.Chain, string) (string, error) {
	return "", nil
}
func (m *mockBridgeForKeysign) GetReferenceMemoByTxHash(string) (string, error) { return "", nil }

// ---------------------------------------------------------------------------
// KeysignError Tests
// ---------------------------------------------------------------------------

type KeysignErrorSuite struct{}

var _ = Suite(&KeysignErrorSuite{})

func (s *KeysignErrorSuite) TestNewKeysignError(c *C) {
	b := stypes.Blame{
		FailReason: "test reason",
		Round:      "round1",
		BlameNodes: []stypes.Node{{Pubkey: "pk1"}},
	}
	ke := NewKeysignError(b)
	c.Assert(ke.Blame, DeepEquals, b)
}

func (s *KeysignErrorSuite) TestKeysignErrorString(c *C) {
	b := stypes.Blame{
		FailReason: "something went wrong",
		Round:      "round2",
		BlameNodes: []stypes.Node{{Pubkey: "pk1"}, {Pubkey: "pk2"}},
	}
	ke := NewKeysignError(b)
	errStr := ke.Error()
	c.Assert(errStr, Matches, ".*something went wrong.*round2.*pk1.*pk2.*")
}

func (s *KeysignErrorSuite) TestIsTerminalRound(c *C) {
	// This legacy-name coverage complements the protocol-specific secp256k1,
	// ed25519, and FROST terminal-boundary cases in round_normalizer_test.go.
	// v1's SignRound7Message is no longer accepted — a v3 node will never emit
	// it for its own terminal failure.
	ke := NewKeysignError(stypes.Blame{Round: "SignRound9Message"})
	c.Assert(ke.IsTerminalRound(), Equals, true)

	ke2 := NewKeysignError(stypes.Blame{Round: "SignRound7Message"})
	c.Assert(ke2.IsTerminalRound(), Equals, false)

	ke3 := NewKeysignError(stypes.Blame{Round: "SignRound3Message"})
	c.Assert(ke3.IsTerminalRound(), Equals, false)

	ke4 := NewKeysignError(stypes.Blame{})
	c.Assert(ke4.IsTerminalRound(), Equals, false)

	ke5 := NewKeysignError(stypes.Blame{Round: tssMessages.FROSTKEYSIGNSHARE})
	c.Assert(ke5.IsTerminalRound(), Equals, true)
}

// ---------------------------------------------------------------------------
// KeySign (tss_signer) Tests
// ---------------------------------------------------------------------------

func (s *KeySignTestSuite) TestNewKeySign(c *C) {
	server := &mockTssServer{}
	bridge := &mockBridgeForKeysign{}
	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	c.Assert(ks, NotNil)
	c.Assert(ks.server, Equals, server)
}

func (s *KeySignTestSuite) TestGetPrivKey(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})
	c.Assert(ks.GetPrivKey(), IsNil)
}

func (s *KeySignTestSuite) TestGetAddr(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})
	c.Assert(ks.GetAddr(), IsNil)
}

func (s *KeySignTestSuite) TestExportAsMnemonic(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})
	mnemonic, err := ks.ExportAsMnemonic()
	c.Assert(err, IsNil)
	c.Assert(mnemonic, Equals, "")
}

func (s *KeySignTestSuite) TestExportAsPrivateKey(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})
	pk, err := ks.ExportAsPrivateKey()
	c.Assert(err, IsNil)
	c.Assert(pk, Equals, "")
}

func (s *KeySignTestSuite) TestExportAsKeyStore(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})
	keyStore, err := ks.ExportAsKeyStore("password")
	c.Assert(err, IsNil)
	c.Assert(keyStore, IsNil)
}

func (s *KeySignTestSuite) TestGetSignature(c *C) {
	rVal := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20})
	sVal := base64.StdEncoding.EncodeToString([]byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f, 0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37, 0x38, 0x39, 0x3a, 0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40})
	sig, err := getSignature(rVal, sVal)
	c.Assert(err, IsNil)
	c.Assert(len(sig), Equals, 64)

	// Invalid R base64
	_, err = getSignature("invalid-base64!!!", sVal)
	c.Assert(err, NotNil)

	// Invalid S base64
	_, err = getSignature(rVal, "invalid-base64!!!")
	c.Assert(err, NotNil)
}

func (s *KeySignTestSuite) TestGetSignatureHighS(c *C) {
	rBytes := make([]byte, 32)
	rBytes[0] = 0x01
	rVal := base64.StdEncoding.EncodeToString(rBytes)

	// S value larger than halfOrder should be normalized
	sBytes := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE, 0xBA, 0xAE, 0xDC, 0xE6, 0xAF, 0x48, 0xA0, 0x3B, 0xBF, 0xD2, 0x5E, 0x8C, 0xD0, 0x36, 0x41, 0x40}
	sVal := base64.StdEncoding.EncodeToString(sBytes)
	sig, err := getSignature(rVal, sVal)
	c.Assert(err, IsNil)
	c.Assert(len(sig), Equals, 64)
}

func (s *KeySignTestSuite) TestRemoteSignEmptyMsg(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})
	sig, recoveryID, err := ks.RemoteSign(nil, common.SigningAlgoSecp256k1, "poolpub", 0)
	c.Assert(err, IsNil)
	c.Assert(sig, IsNil)
	c.Assert(recoveryID, IsNil)

	sig, recoveryID, err = ks.RemoteSign([]byte{}, common.SigningAlgoSecp256k1, "poolpub", 0)
	c.Assert(err, IsNil)
	c.Assert(sig, IsNil)
	c.Assert(recoveryID, IsNil)
}

func (s *KeySignTestSuite) TestRemoteSignSecp256k1Success(c *C) {
	msg := []byte("test msg")
	encodedMsg := base64.StdEncoding.EncodeToString(msg)

	rBytes := make([]byte, 32)
	rBytes[0] = 0x01
	sBytes := make([]byte, 32)
	sBytes[0] = 0x02
	rB64 := base64.StdEncoding.EncodeToString(rBytes)
	sB64 := base64.StdEncoding.EncodeToString(sBytes)
	recoveryB64 := base64.StdEncoding.EncodeToString([]byte{0x00})

	server := &mockTssServer{
		resp: keysign.Response{
			Signatures: []keysign.Signature{
				{Msg: encodedMsg, R: rB64, S: sB64, RecoveryID: recoveryB64},
			},
			Status: tsscommon.Success,
		},
	}

	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	sig, recovery, err := ks.RemoteSign(msg, common.SigningAlgoSecp256k1, "poolpub", 100)
	c.Assert(err, IsNil)
	c.Assert(sig, NotNil)
	c.Assert(len(sig), Equals, 64)
	c.Assert(recovery, NotNil)
}

func (s *KeySignTestSuite) TestRemoteSignUsesBatchInterval(c *C) {
	msg := []byte("test msg")
	sigBytes := make([]byte, 64)
	sigBytes[0] = 0x01
	server := &mockTssServer{
		resp: keysign.Response{
			Signatures: []keysign.Signature{
				{Msg: base64.StdEncoding.EncodeToString(msg), S: base64.StdEncoding.EncodeToString(sigBytes)},
			},
			Status: tsscommon.Success,
		},
	}
	ks, err := NewKeySign(server, &mockBridgeForKeysign{version: semver.MustParse("1.0.0")})
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	start := time.Now()
	sig, _, err := ks.RemoteSign(msg, common.SigningAlgoEd25519, "poolpub", 100)
	elapsed := time.Since(start)
	c.Assert(err, IsNil)
	c.Assert(sig, DeepEquals, sigBytes)
	// The default interval alone takes a second.
	c.Assert(elapsed < time.Second, Equals, true, Commentf("keysign took %s, want it to follow KeySignBatchInterval (%s)", elapsed, KeySignBatchInterval))
}

func (s *KeySignTestSuite) TestRemoteSignEd25519Success(c *C) {
	msg := []byte("test msg")
	encodedMsg := base64.StdEncoding.EncodeToString(msg)

	sigBytes := make([]byte, 64)
	sigBytes[0] = 0x01
	sB64 := base64.StdEncoding.EncodeToString(sigBytes)

	server := &mockTssServer{
		resp: keysign.Response{
			Signatures: []keysign.Signature{
				{Msg: encodedMsg, S: sB64},
			},
			Status: tsscommon.Success,
		},
	}

	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	sig, recovery, err := ks.RemoteSign(msg, common.SigningAlgoEd25519, "poolpub", 100)
	c.Assert(err, IsNil)
	c.Assert(sig, NotNil)
	c.Assert(len(sig), Equals, 64)
	c.Assert(recovery, IsNil)
}

func (s *KeySignTestSuite) TestRemoteSignError(c *C) {
	server := &mockTssServer{
		err: fmt.Errorf("keysign failed"),
	}

	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	_, _, err = ks.RemoteSign([]byte("test msg"), common.SigningAlgoSecp256k1, "poolpub", 100)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*fail to tss sign.*")
}

func (s *KeySignTestSuite) TestRemoteSignBlame(c *C) {
	server := &mockTssServer{
		resp: keysign.Response{
			Status: tsscommon.Fail,
			Blame: blame.Blame{
				FailReason: "timeout",
				Round:      "round1",
				BlameNodes: []blame.Node{{Pubkey: "pk1"}},
			},
		},
	}

	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	_, _, err = ks.RemoteSign([]byte("test msg"), common.SigningAlgoSecp256k1, "poolpub", 100)
	c.Assert(err, NotNil)
	// The error is wrapped via fmt.Errorf, so use errors.As
	var ke KeysignError
	c.Assert(errors.As(err, &ke), Equals, true)
	c.Assert(ke.Blame.FailReason, Equals, "timeout")
	c.Assert(len(ke.Blame.BlameNodes), Equals, 1)
}

func (s *KeySignTestSuite) TestRemoteSignNotSelected(c *C) {
	msg := []byte("test msg")
	encodedMsg := base64.StdEncoding.EncodeToString(msg)

	server := &mockTssServer{
		resp: keysign.Response{
			Signatures: []keysign.Signature{
				{Msg: encodedMsg, R: "", S: ""},
			},
			Status: tsscommon.Success,
		},
	}

	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	sig, recovery, err := ks.RemoteSign(msg, common.SigningAlgoSecp256k1, "poolpub", 100)
	c.Assert(errors.Is(err, ErrNotSelectedForKeysign), Equals, true)
	c.Assert(sig, IsNil)
	c.Assert(recovery, IsNil)
}

// TestRemoteSignEd25519NotSelected verifies EdDSA signing returns the same
// stand-down error as ECDSA when this node is not in the policy signer set.
func (s *KeySignTestSuite) TestRemoteSignEd25519NotSelected(c *C) {
	msg := []byte("test msg")
	encodedMsg := base64.StdEncoding.EncodeToString(msg)

	server := &mockTssServer{
		resp: keysign.Response{
			Signatures: []keysign.Signature{
				{Msg: encodedMsg},
			},
			Status: tsscommon.Success,
		},
	}

	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	sig, recovery, err := ks.RemoteSign(msg, common.SigningAlgoEd25519, "poolpub", 100)
	c.Assert(errors.Is(err, ErrNotSelectedForKeysign), Equals, true)
	c.Assert(sig, IsNil)
	c.Assert(recovery, IsNil)
}

func (s *KeySignTestSuite) TestRemoteSignRequiresHeight(c *C) {
	server := &mockTssServer{}
	bridge := &mockBridgeForKeysign{
		version: semver.MustParse("1.0.0"),
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	_, _, err = ks.RemoteSign([]byte("test msg"), common.SigningAlgoSecp256k1, "poolpub", 0)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*keysign task height must be positive.*")
	c.Assert(bridge.keysignPartyCalls, Equals, 0)
}

func (s *KeySignTestSuite) TestRemoteSignUsesRequestedHeight(c *C) {
	server := &mockTssServer{}
	bridge := &mockBridgeForKeysign{
		version:  semver.MustParse("1.0.0"),
		blockErr: fmt.Errorf("unexpected block height lookup"),
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	_, _, err = ks.RemoteSign([]byte("test msg"), common.SigningAlgoSecp256k1, "poolpub", 456)
	c.Assert(errors.Is(err, ErrNotSelectedForKeysign), Equals, true)
	c.Assert(bridge.keysignPartyCalls, Equals, 1)
	c.Assert(bridge.lastKeysignPartyBlock, Equals, int64(456))
}

func (s *KeySignTestSuite) TestRemoteSignMsgNotFound(c *C) {
	rBytes := make([]byte, 32)
	rBytes[0] = 0x01
	sBytes := make([]byte, 32)
	sBytes[0] = 0x02
	rB64 := base64.StdEncoding.EncodeToString(rBytes)
	sB64 := base64.StdEncoding.EncodeToString(sBytes)

	server := &mockTssServer{
		resp: keysign.Response{
			Signatures: []keysign.Signature{
				{Msg: "different_msg", R: rB64, S: sB64, RecoveryID: base64.StdEncoding.EncodeToString([]byte{0x00})},
			},
			Status: tsscommon.Success,
		},
	}

	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}

	ks, err := NewKeySign(server, bridge)
	c.Assert(err, IsNil)
	ks.Start()
	defer ks.Stop()

	_, _, err = ks.RemoteSign([]byte("test msg"), common.SigningAlgoSecp256k1, "poolpub", 100)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*didn't find signature.*")
}

// TestBuildKeysignRequestAttachesKeysignPolicy verifies that bifrost passes the
// filtered policy from thornode through to go-tss without re-deriving it locally.
func (s *KeySignTestSuite) TestBuildKeysignRequestAttachesKeysignPolicy(c *C) {
	poolPubKey := stypes.GetRandomPubKey()
	signers := common.PubKeys{stypes.GetRandomPubKey(), stypes.GetRandomPubKey()}
	neverSigners := common.PubKeys{stypes.GetRandomPubKey()}
	jailedFallbackSigners := common.PubKeys{stypes.GetRandomPubKey()}
	bridge := &mockBridgeForKeysign{
		version:               semver.MustParse("1.0.0"),
		blockHeight:           123,
		keysignParty:          signers,
		neverSigners:          neverSigners,
		jailedFallbackSigners: jailedFallbackSigners,
		requiredSigners:       2,
	}
	ks, _ := NewKeySign(&mockTssServer{}, bridge)
	tasks := []*tssKeySignTask{
		{Msg: base64.StdEncoding.EncodeToString([]byte("msg1")), Height: 123},
	}

	req, err := ks.buildKeysignRequest(poolPubKey.String(), common.SigningAlgoSecp256k1, tasks)
	c.Assert(err, IsNil)
	c.Assert(req.BlockHeight, Equals, int64(123))
	c.Assert(fmt.Sprint(req.SignerPubKeys), Equals, fmt.Sprint(signers.Strings()))
	c.Assert(fmt.Sprint(req.NeverSigners), Equals, fmt.Sprint(neverSigners.Strings()))
	c.Assert(fmt.Sprint(req.JailedFallbackSigners), Equals, fmt.Sprint(jailedFallbackSigners.Strings()))
	c.Assert(req.RequiredSigners, Equals, 2)
	c.Assert(bridge.keysignPartyCalls, Equals, 1)
	c.Assert(bridge.lastKeysignPartyKey.Equals(poolPubKey), Equals, true)
	c.Assert(bridge.lastKeysignPartyBlock, Equals, int64(123))
}

// TestBuildKeysignRequestAllowsEmptyPolicy preserves the empty-policy path used
// by pre-vault keygen checks that do not filter signers.
func (s *KeySignTestSuite) TestBuildKeysignRequestAllowsEmptyPolicy(c *C) {
	poolPubKey := stypes.GetRandomPubKey()
	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 123,
	}
	ks, _ := NewKeySign(&mockTssServer{}, bridge)
	tasks := []*tssKeySignTask{
		{Msg: base64.StdEncoding.EncodeToString([]byte("msg1")), Height: 123},
	}

	req, err := ks.buildKeysignRequest(poolPubKey.String(), common.SigningAlgoSecp256k1, tasks)
	c.Assert(err, IsNil)
	c.Assert(req.BlockHeight, Equals, int64(123))
	c.Assert(req.SignerPubKeys, HasLen, 0)
	c.Assert(bridge.keysignPartyCalls, Equals, 1)
}

func (s *KeySignTestSuite) TestBuildKeysignRequestRejectsMixedHeights(c *C) {
	poolPubKey := stypes.GetRandomPubKey()
	bridge := &mockBridgeForKeysign{
		version: semver.MustParse("1.0.0"),
	}
	ks, _ := NewKeySign(&mockTssServer{}, bridge)
	tasks := []*tssKeySignTask{
		{Msg: base64.StdEncoding.EncodeToString([]byte("msg1")), Height: 123},
		{Msg: base64.StdEncoding.EncodeToString([]byte("msg2")), Height: 124},
	}

	_, err := ks.buildKeysignRequest(poolPubKey.String(), common.SigningAlgoSecp256k1, tasks)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*mixed keysign task heights.*")
	c.Assert(bridge.keysignPartyCalls, Equals, 0)
}

func (s *KeySignTestSuite) TestGetVersionCached(c *C) {
	bridge := &mockBridgeForKeysign{
		version: semver.MustParse("2.0.0"),
	}
	ks, _ := NewKeySign(&mockTssServer{}, bridge)

	v := ks.getVersion()
	c.Assert(v.String(), Equals, "2.0.0")

	// Second call within block time should use cache
	bridge.version = semver.MustParse("3.0.0")
	v = ks.getVersion()
	c.Assert(v.String(), Equals, "2.0.0")
}

func (s *KeySignTestSuite) TestGetVersionError(c *C) {
	bridge := &mockBridgeForKeysign{
		versionErr: fmt.Errorf("fail"),
	}
	ks, _ := NewKeySign(&mockTssServer{}, bridge)

	v := ks.getVersion()
	c.Assert(v.String(), Equals, "0.0.0")
}

func (s *KeySignTestSuite) TestSetTssKeySignTasksFail(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})

	tasks := []*tssKeySignTask{
		{Resp: make(chan tssKeySignResult, 1)},
		{Resp: make(chan tssKeySignResult, 1)},
	}

	testErr := fmt.Errorf("test error")
	ks.setTssKeySignTasksFail(tasks, testErr)

	for _, t := range tasks {
		select {
		case result := <-t.Resp:
			c.Assert(result.Err, Equals, testErr)
		default:
			c.Fatal("expected result on channel")
		}
	}
}

func (s *KeySignTestSuite) TestStartStop(c *C) {
	ks, _ := NewKeySign(&mockTssServer{}, &mockBridgeForKeysign{})
	ks.Start()
	time.Sleep(50 * time.Millisecond)
	ks.Stop()
}

func (s *KeySignTestSuite) TestToLocalTSSSignerSuccess(c *C) {
	msg1 := base64.StdEncoding.EncodeToString([]byte("msg1"))

	rBytes := make([]byte, 32)
	rBytes[0] = 0x01
	sBytes := make([]byte, 32)
	sBytes[0] = 0x02
	rB64 := base64.StdEncoding.EncodeToString(rBytes)
	sB64 := base64.StdEncoding.EncodeToString(sBytes)
	recoveryB64 := base64.StdEncoding.EncodeToString([]byte{0x00})

	server := &mockTssServer{
		resp: keysign.Response{
			Signatures: []keysign.Signature{
				{Msg: msg1, R: rB64, S: sB64, RecoveryID: recoveryB64},
			},
			Status: tsscommon.Success,
		},
	}
	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}
	ks, _ := NewKeySign(server, bridge)

	tasks := []*tssKeySignTask{
		{PoolPubKey: "poolpub", Algo: common.SigningAlgoSecp256k1, Msg: msg1, Height: 100, Resp: make(chan tssKeySignResult, 1)},
	}

	ks.wg.Add(1)
	ks.toLocalTSSSigner("poolpub", common.SigningAlgoSecp256k1, tasks)

	result := <-tasks[0].Resp
	c.Assert(result.Err, IsNil)
	c.Assert(result.R, Equals, rB64)
	c.Assert(result.S, Equals, sB64)
}

// TestToLocalTSSSignerNotSelectedEmptyResponse verifies an empty go-tss
// response is treated as a non-error stand-down path for unselected signers.
func (s *KeySignTestSuite) TestToLocalTSSSignerNotSelectedEmptyResponse(c *C) {
	msg1 := base64.StdEncoding.EncodeToString([]byte("msg1"))
	server := &mockTssServer{
		resp: keysign.Response{},
	}
	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}
	ks, _ := NewKeySign(server, bridge)

	tasks := []*tssKeySignTask{
		{PoolPubKey: "poolpub", Algo: common.SigningAlgoSecp256k1, Msg: msg1, Height: 100, Resp: make(chan tssKeySignResult, 1)},
	}

	ks.wg.Add(1)
	ks.toLocalTSSSigner("poolpub", common.SigningAlgoSecp256k1, tasks)

	result := <-tasks[0].Resp
	c.Assert(result.Err, IsNil)
	c.Assert(result.R, Equals, "")
	c.Assert(result.S, Equals, "")
}

func (s *KeySignTestSuite) TestToLocalTSSSignerServerError(c *C) {
	server := &mockTssServer{
		err: fmt.Errorf("server error"),
	}
	bridge := &mockBridgeForKeysign{
		version:     semver.MustParse("1.0.0"),
		blockHeight: 100,
	}
	ks, _ := NewKeySign(server, bridge)

	tasks := []*tssKeySignTask{
		{PoolPubKey: "poolpub", Algo: common.SigningAlgoSecp256k1, Msg: "msg1", Height: 100, Resp: make(chan tssKeySignResult, 1)},
	}

	ks.wg.Add(1)
	ks.toLocalTSSSigner("poolpub", common.SigningAlgoSecp256k1, tasks)

	result := <-tasks[0].Resp
	c.Assert(result.Err, NotNil)
	c.Assert(result.Err.Error(), Matches, ".*fail tss keysign.*")
}

func (s *KeySignTestSuite) TestToLocalTSSSignerMissingHeightError(c *C) {
	server := &mockTssServer{}
	bridge := &mockBridgeForKeysign{
		version: semver.MustParse("1.0.0"),
	}
	ks, _ := NewKeySign(server, bridge)

	tasks := []*tssKeySignTask{
		{PoolPubKey: "poolpub", Algo: common.SigningAlgoSecp256k1, Msg: "msg1", Resp: make(chan tssKeySignResult, 1)},
	}

	ks.wg.Add(1)
	ks.toLocalTSSSigner("poolpub", common.SigningAlgoSecp256k1, tasks)

	result := <-tasks[0].Resp
	c.Assert(result.Err, NotNil)
	c.Assert(result.Err.Error(), Matches, ".*keysign task height must be positive.*")
}

// A local history/control failure must never enter TSS or become peer blame.
// A subsequent retry at the same item height must remain possible.
func (s *KeySignTestSuite) TestToLocalTSSSignerPolicyReadFailure(c *C) {
	for _, algo := range []common.SigningAlgo{common.SigningAlgoSecp256k1, common.SigningAlgoEd25519} {
		readErr := errors.New("historical store unavailable at height 100")
		server := &mockTssServer{}
		bridge := &mockBridgeForKeysign{keysignPartyErr: readErr}
		ks, err := NewKeySign(server, bridge)
		c.Assert(err, IsNil)
		task := &tssKeySignTask{PoolPubKey: "poolpub", Algo: algo, Msg: "msg1", Height: 100, Resp: make(chan tssKeySignResult, 1)}
		ks.wg.Add(1)
		ks.toLocalTSSSigner("poolpub", algo, []*tssKeySignTask{task})
		result := <-task.Resp
		c.Assert(errors.Is(result.Err, readErr), Equals, true)
		var blameErr KeysignError
		c.Assert(errors.As(result.Err, &blameErr), Equals, false)
		c.Assert(server.lastReq.PoolPubKey, Equals, "")
		c.Assert(bridge.lastKeysignPartyBlock, Equals, int64(100))

		bridge.keysignPartyErr = nil
		ks.wg.Add(1)
		ks.toLocalTSSSigner("poolpub", algo, []*tssKeySignTask{task})
		c.Assert((<-task.Resp).Err, IsNil)
		c.Assert(server.lastReq.PoolPubKey, Equals, "poolpub")
		c.Assert(server.lastReq.BlockHeight, Equals, int64(100))
	}
}

// ---------------------------------------------------------------------------
// encryptAES / decryptAES tests
// ---------------------------------------------------------------------------

type AESTestSuite struct{}

var _ = Suite(&AESTestSuite{})

func (s *AESTestSuite) TestEncryptDecryptAES(c *C) {
	key := make([]byte, 32)
	key[0] = 0x01
	data := []byte("hello world test data for aes encryption")

	encrypted, err := encryptAES(key, data)
	c.Assert(err, IsNil)
	c.Assert(encrypted, Not(DeepEquals), data)

	decrypted, err := decryptAES(key, encrypted)
	c.Assert(err, IsNil)
	c.Assert(string(decrypted), Equals, string(data))
}

func (s *AESTestSuite) TestEncryptAESBadKeyLen(c *C) {
	key := make([]byte, 16)
	_, err := encryptAES(key, []byte("data"))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "failed to encrypt: key must be 32 bytes")
}

func (s *AESTestSuite) TestDecryptAESBadKeyLen(c *C) {
	key := make([]byte, 16)
	_, err := decryptAES(key, []byte("data"))
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "failed to decrypt: key must be 32 bytes")
}

func (s *AESTestSuite) TestDecryptAESBadData(c *C) {
	key := make([]byte, 32)
	key[0] = 0x01
	// Data needs to be at least nonce size (12 for AES-GCM) + some ciphertext, but garbage
	badData := make([]byte, 32)
	for i := range badData {
		badData[i] = byte(i)
	}
	_, err := decryptAES(key, badData)
	c.Assert(err, NotNil)
}

func (s *AESTestSuite) TestDecryptAEADBadCiphertext(c *C) {
	key := make([]byte, 32)
	key[0] = 0x01

	encrypted, err := encryptAES(key, []byte("test data"))
	c.Assert(err, IsNil)

	encrypted[len(encrypted)-1] ^= 0xFF
	_, err = decryptAES(key, encrypted)
	c.Assert(err, NotNil)
}
