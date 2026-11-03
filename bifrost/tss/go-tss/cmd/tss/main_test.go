package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	. "gopkg.in/check.v1"
)

func (TssHttpServerTestSuite) TestResolveTssAuthSecretDisabled(c *C) {
	secret, err := resolveTssAuthSecret("", false, bufio.NewReader(strings.NewReader("")))
	c.Assert(err, IsNil)
	c.Assert(secret, Equals, "")
}

func (TssHttpServerTestSuite) TestResolveTssAuthSecretFromFile(c *C) {
	secretPath := filepath.Join(c.MkDir(), "tss-auth-secret")
	c.Assert(os.WriteFile(secretPath, []byte("  file-secret\n"), 0o600), IsNil)

	secret, err := resolveTssAuthSecret(secretPath, false, bufio.NewReader(strings.NewReader("")))
	c.Assert(err, IsNil)
	c.Assert(secret, Equals, "file-secret")
}

func (TssHttpServerTestSuite) TestResolveTssAuthSecretRejectsEmptyFile(c *C) {
	secretPath := filepath.Join(c.MkDir(), "tss-auth-secret")
	c.Assert(os.WriteFile(secretPath, []byte("\n\t"), 0o600), IsNil)

	secret, err := resolveTssAuthSecret(secretPath, false, bufio.NewReader(strings.NewReader("")))
	c.Assert(secret, Equals, "")
	c.Assert(err, ErrorMatches, `tss auth secret file ".*" is empty`)
}

func (TssHttpServerTestSuite) TestResolveTssAuthSecretFromPrompt(c *C) {
	secret, err := resolveTssAuthSecret("", true, bufio.NewReader(strings.NewReader("prompt-secret\n")))
	c.Assert(err, IsNil)
	c.Assert(secret, Equals, "prompt-secret")
}

func (TssHttpServerTestSuite) TestResolveTssAuthSecretRejectsFileAndPrompt(c *C) {
	secret, err := resolveTssAuthSecret("secret-file", true, bufio.NewReader(strings.NewReader("")))
	c.Assert(secret, Equals, "")
	c.Assert(err, ErrorMatches, ".*mutually exclusive")
}
