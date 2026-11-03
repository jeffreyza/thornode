package log

import (
	"bytes"
	"testing"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestPackage(t *testing.T) { TestingT(t) }

type LogSuite struct{}

var _ = Suite(&LogSuite{})

func newTestLogger(buf *bytes.Buffer, level zerolog.Level) SdkLogWrapper {
	logger := zerolog.New(buf).Level(level)
	return SdkLogWrapper{Logger: &logger}
}

func (s *LogSuite) TestSdkLogWrapper_Info(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)
	w.Info("hello", "key", "value")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("hello")), Equals, true)
	c.Assert(bytes.Contains(buf.Bytes(), []byte("key")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_Warn(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)
	w.Warn("warning msg", "k", "v")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("warning msg")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_Error(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)
	w.Error("error msg", "k", "v")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("error msg")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_Debug(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.DebugLevel)
	w.Debug("debug msg", "k", "v")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("debug msg")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_Debug_FilteredByLevel(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)
	w.Debug("should not appear")
	c.Assert(buf.Len(), Equals, 0)
}

func (s *LogSuite) TestSdkLogWrapper_With_OddKeyVals(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)
	child := w.With("odd")
	c.Assert(child, NotNil)
	cw, ok := child.(SdkLogWrapper)
	c.Assert(ok, Equals, true)
	cw.Info("from child")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("from child")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_With_DebugLevel(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.DebugLevel)
	child := w.With("module", "test-module")
	c.Assert(child, NotNil)
	cw, ok := child.(SdkLogWrapper)
	c.Assert(ok, Equals, true)
	cw.Debug("debug with module")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("debug with module")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_With_NonStringKey(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)
	child := w.With(123, "value")
	c.Assert(child, NotNil)
}

func (s *LogSuite) TestSdkLogWrapper_With_ModuleFiltering(c *C) {
	config.Init()

	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)

	child := w.With("module", "consensus")
	cw, ok := child.(SdkLogWrapper)
	c.Assert(ok, Equals, true)
	cw.Info("this should be filtered")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("this should be filtered")), Equals, false)
}

func (s *LogSuite) TestSdkLogWrapper_With_ModuleNotFiltered(c *C) {
	config.Init()

	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)

	child := w.With("module", "allowed-mod")
	cw, ok := child.(SdkLogWrapper)
	c.Assert(ok, Equals, true)
	cw.Info("should appear")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("should appear")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_With_ModuleNonStringValue(c *C) {
	config.Init()

	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)

	child := w.With("module", 42)
	c.Assert(child, NotNil)
}

func (s *LogSuite) TestSdkLogWrapper_With_NonModuleKey(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)

	child := w.With("other-key", "other-value")
	cw, ok := child.(SdkLogWrapper)
	c.Assert(ok, Equals, true)
	cw.Info("msg with context")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("msg with context")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_Impl(c *C) {
	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)
	impl := w.Impl()
	c.Assert(impl, NotNil)
	_, ok := impl.(*zerolog.Logger)
	c.Assert(ok, Equals, true)
}

func (s *LogSuite) TestGetLogFields(c *C) {
	// Odd number of args returns nil
	result := getLogFields("key")
	c.Assert(result, IsNil)

	// Even number with valid string keys
	result = getLogFields("key1", "val1", "key2", 42)
	c.Assert(result, NotNil)
	c.Assert(result["key1"], Equals, "val1")
	c.Assert(result["key2"], Equals, 42)

	// Non-string key is skipped
	result = getLogFields(123, "val")
	c.Assert(result, HasLen, 0)

	// Empty args
	result = getLogFields()
	c.Assert(result, NotNil)
	c.Assert(result, HasLen, 0)
}

func (s *LogSuite) TestSdkLogWrapper_Filter(c *C) {
	config.Init()

	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.InfoLevel)

	w.Info("executed block")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("executed block")), Equals, false)

	w.Info("keep-this")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("keep-this")), Equals, true)
}

func (s *LogSuite) TestSdkLogWrapper_Filter_DebugLevel(c *C) {
	config.Init()

	var buf bytes.Buffer
	w := newTestLogger(&buf, zerolog.DebugLevel)
	w.Info("executed block")
	c.Assert(bytes.Contains(buf.Bytes(), []byte("executed block")), Equals, true)
}
