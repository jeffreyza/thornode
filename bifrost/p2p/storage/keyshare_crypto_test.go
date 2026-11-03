package storage

import (
	"bytes"
	"crypto/sha256"
	"fmt"

	. "gopkg.in/check.v1"
)

type KeyshareCryptoSuite struct{}

var _ = Suite(&KeyshareCryptoSuite{})

func (s *KeyshareCryptoSuite) TestDeriveAtRestKey(c *C) {
	key := DeriveAtRestKey("test phrase")
	expected := sha256.Sum256([]byte(fmt.Sprintf("test phrase+%d", SaltAtRest)))
	c.Assert(key, DeepEquals, expected[:])
	c.Assert(len(key), Equals, 32)
}

func (s *KeyshareCryptoSuite) TestDeriveAtRestKey_Deterministic(c *C) {
	k1 := DeriveAtRestKey("same phrase")
	k2 := DeriveAtRestKey("same phrase")
	c.Assert(k1, DeepEquals, k2)
}

func (s *KeyshareCryptoSuite) TestDeriveAtRestKey_DifferentFromOtherSalts(c *C) {
	key4 := DeriveAtRestKey("test phrase")
	for _, salt := range []int{1, 2, 3} {
		other := sha256.Sum256([]byte(fmt.Sprintf("test phrase+%d", salt)))
		c.Assert(bytes.Equal(key4, other[:]), Equals, false)
	}
}

func (s *KeyshareCryptoSuite) TestEncryptDecryptRoundTrip(c *C) {
	key := DeriveAtRestKey("test phrase")
	plaintext := []byte(`{"pub_key":"thorpub1test","local_data":"dGVzdA=="}`)

	encrypted, err := EncryptAtRest(key, plaintext)
	c.Assert(err, IsNil)
	c.Assert(IsEncryptedAtRest(encrypted), Equals, true)
	c.Assert(bytes.Equal(encrypted, plaintext), Equals, false)

	decrypted, err := DecryptAtRest(key, encrypted)
	c.Assert(err, IsNil)
	c.Assert(decrypted, DeepEquals, plaintext)
}

func (s *KeyshareCryptoSuite) TestEncryptProducesUniqueNonces(c *C) {
	key := DeriveAtRestKey("test phrase")
	plaintext := []byte("same data")

	enc1, err := EncryptAtRest(key, plaintext)
	c.Assert(err, IsNil)
	enc2, err := EncryptAtRest(key, plaintext)
	c.Assert(err, IsNil)

	c.Assert(bytes.Equal(enc1, enc2), Equals, false)
}

func (s *KeyshareCryptoSuite) TestDecryptWrongKey(c *C) {
	key := DeriveAtRestKey("correct phrase")
	wrongKey := DeriveAtRestKey("wrong phrase")
	plaintext := []byte("secret data")

	encrypted, err := EncryptAtRest(key, plaintext)
	c.Assert(err, IsNil)

	_, err = DecryptAtRest(wrongKey, encrypted)
	c.Assert(err, NotNil)
}

func (s *KeyshareCryptoSuite) TestDecryptTamperedData(c *C) {
	key := DeriveAtRestKey("test phrase")
	plaintext := []byte("secret data")

	encrypted, err := EncryptAtRest(key, plaintext)
	c.Assert(err, IsNil)

	encrypted[len(encrypted)-1] ^= 0xff
	_, err = DecryptAtRest(key, encrypted)
	c.Assert(err, NotNil)
}

func (s *KeyshareCryptoSuite) TestDecryptTruncatedData(c *C) {
	key := DeriveAtRestKey("test phrase")
	short := append(magicAtRest[:], 0x00)
	_, err := DecryptAtRest(key, short)
	c.Assert(err, NotNil)
}

func (s *KeyshareCryptoSuite) TestDecryptNoMagic(c *C) {
	key := DeriveAtRestKey("test phrase")
	_, err := DecryptAtRest(key, []byte("plaintext json"))
	c.Assert(err, NotNil)
}

func (s *KeyshareCryptoSuite) TestIsEncryptedAtRest(c *C) {
	c.Assert(IsEncryptedAtRest(magicAtRest[:]), Equals, true)
	c.Assert(IsEncryptedAtRest(append(magicAtRest[:], 0x00, 0x01)), Equals, true)
	c.Assert(IsEncryptedAtRest([]byte("{")), Equals, false)
	c.Assert(IsEncryptedAtRest([]byte("TK")), Equals, false)
	c.Assert(IsEncryptedAtRest(nil), Equals, false)
	c.Assert(IsEncryptedAtRest([]byte{}), Equals, false)
}

func (s *KeyshareCryptoSuite) TestEncryptEmptyPlaintext(c *C) {
	key := DeriveAtRestKey("test phrase")
	encrypted, err := EncryptAtRest(key, []byte{})
	c.Assert(err, IsNil)
	c.Assert(IsEncryptedAtRest(encrypted), Equals, true)

	decrypted, err := DecryptAtRest(key, encrypted)
	c.Assert(err, IsNil)
	c.Assert(len(decrypted), Equals, 0)
}
