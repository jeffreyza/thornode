package common

import (
	. "gopkg.in/check.v1"
)

type sessionNonceSuite struct{}

var _ = Suite(&sessionNonceSuite{})

var nonceTestKeys = []string{
	"thorpub1addwnpepqtdklw8tf3anjz7nn5fly3uvq2e67w2apn560s4smmrt9e3x52nt2svmmu3",
	"thorpub1addwnpepqtspqyy6gk22u37ztra4hq3hdakc0w0k60sfy849mlml2vrpfr0wvm6uz09",
	"thorpub1addwnpepq2ryyje5zr09lq7gqptjwnxqsy2vcdngvwd6z7yt5yjcnyj8c8cn559xe6y",
	"thorpub1addwnpepqfjcw5l4ay5t00c32mmlky7qrppepxzdlkcwfs2fd5u73qrwna0vzag3y4j",
}

// The nonce must be byte-identical when derived on two "nodes" from the
// same ceremony inputs, regardless of the order the inputs arrive in.
func (s *sessionNonceSuite) TestKeygenNonceDeterministic(c *C) {
	nodeA := KeygenSessionNonce(nonceTestKeys, 1024, "secp256k1")

	reversed := []string{nonceTestKeys[3], nonceTestKeys[2], nonceTestKeys[1], nonceTestKeys[0]}
	nodeB := KeygenSessionNonce(reversed, 1024, "secp256k1")

	c.Assert(nodeA.Cmp(nodeB), Equals, 0)
	c.Assert(nodeA.Bytes(), DeepEquals, nodeB.Bytes())
	c.Assert(nodeA.Sign(), Equals, 1)
	// input order must be preserved (sorting happens on a copy)
	c.Assert(reversed[0], Equals, nonceTestKeys[3])
}

func (s *sessionNonceSuite) TestKeygenNonceUniquePerCeremony(c *C) {
	base := KeygenSessionNonce(nonceTestKeys, 1024, "secp256k1")

	otherHeight := KeygenSessionNonce(nonceTestKeys, 1025, "secp256k1")
	c.Assert(base.Cmp(otherHeight), Not(Equals), 0)

	otherAlgo := KeygenSessionNonce(nonceTestKeys, 1024, "ed25519")
	c.Assert(base.Cmp(otherAlgo), Not(Equals), 0)

	otherCommittee := KeygenSessionNonce(nonceTestKeys[:3], 1024, "secp256k1")
	c.Assert(base.Cmp(otherCommittee), Not(Equals), 0)
}

func (s *sessionNonceSuite) TestKeysignNonceDeterministic(c *C) {
	const pool = "thorpub1addwnpepqtdklw8tf3anjz7nn5fly3uvq2e67w2apn560s4smmrt9e3x52nt2svmmu3"
	const digest = "77000818837576797286404593011809197461402327807342979030326234329536463902463"

	nodeA := KeysignSessionNonce(pool, digest, 0, nonceTestKeys)

	reversed := []string{nonceTestKeys[3], nonceTestKeys[2], nonceTestKeys[1], nonceTestKeys[0]}
	nodeB := KeysignSessionNonce(pool, digest, 0, reversed)

	c.Assert(nodeA.Cmp(nodeB), Equals, 0)
	c.Assert(nodeA.Bytes(), DeepEquals, nodeB.Bytes())
	c.Assert(nodeA.Sign(), Equals, 1)
	c.Assert(reversed[0], Equals, nonceTestKeys[3])
}

func (s *sessionNonceSuite) TestKeysignNonceUniquePerMessage(c *C) {
	const pool = "thorpub1addwnpepqtdklw8tf3anjz7nn5fly3uvq2e67w2apn560s4smmrt9e3x52nt2svmmu3"
	const digest = "77000818837576797286404593011809197461402327807342979030326234329536463902463"

	base := KeysignSessionNonce(pool, digest, 0, nonceTestKeys)

	otherIndex := KeysignSessionNonce(pool, digest, 1, nonceTestKeys)
	c.Assert(base.Cmp(otherIndex), Not(Equals), 0)

	otherDigest := KeysignSessionNonce(pool, digest+"1", 0, nonceTestKeys)
	c.Assert(base.Cmp(otherDigest), Not(Equals), 0)

	otherPool := KeysignSessionNonce(pool+"x", digest, 0, nonceTestKeys)
	c.Assert(base.Cmp(otherPool), Not(Equals), 0)

	// keygen and keysign nonces never collide even on overlapping inputs
	// (distinct domain tags)
	keygenNonce := KeygenSessionNonce(nonceTestKeys, 0, "")
	c.Assert(base.Cmp(keygenNonce), Not(Equals), 0)
}

func (s *sessionNonceSuite) TestSessionNonceEnabled(c *C) {
	c.Assert(SessionNonceEnabled(SessionNonceActivationVersion), Equals, true)
	c.Assert(SessionNonceEnabled("99.0.0"), Equals, true)

	// below activation, empty, or unparseable → nonce stays off
	c.Assert(SessionNonceEnabled("3.19.0"), Equals, false)
	c.Assert(SessionNonceEnabled("0.14.0"), Equals, false)
	c.Assert(SessionNonceEnabled(""), Equals, false)
	c.Assert(SessionNonceEnabled("not-a-version"), Equals, false)
}
