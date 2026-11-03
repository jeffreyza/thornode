package util

import (
	"testing"
	"time"

	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

type Test struct{}

var _ = Suite(&Test{})

func (t *Test) TestFormatDuration(c *C) {
	c.Check(FormatDuration(0*time.Second), Equals, "0s")
	c.Check(FormatDuration(1*time.Second), Equals, "1s")
	c.Check(FormatDuration(59*time.Second), Equals, "59s")
	c.Check(FormatDuration(60*time.Second), Equals, "1m")
	c.Check(FormatDuration(61*time.Second), Equals, "1m 1s")
	c.Check(FormatDuration(119*time.Second), Equals, "1m 59s")
	c.Check(FormatDuration(120*time.Second), Equals, "2m")
	c.Check(FormatDuration(121*time.Second), Equals, "2m 1s")
	c.Check(FormatDuration(3599*time.Second), Equals, "59m 59s")
	c.Check(FormatDuration(3600*time.Second), Equals, "1h")
	c.Check(FormatDuration(3601*time.Second), Equals, "1h 1s")
	c.Check(FormatDuration(3660*time.Second), Equals, "1h 1m")
	c.Check(FormatDuration(3661*time.Second), Equals, "1h 1m 1s")
	c.Check(FormatDuration(7199*time.Second), Equals, "1h 59m 59s")
	c.Check(FormatDuration(7200*time.Second), Equals, "2h")
	c.Check(FormatDuration(7201*time.Second), Equals, "2h 1s")
	c.Check(FormatDuration(7260*time.Second), Equals, "2h 1m")
	c.Check(FormatDuration(7261*time.Second), Equals, "2h 1m 1s")
	c.Check(FormatDuration(86399*time.Second), Equals, "23h 59m 59s")
	c.Check(FormatDuration(86400*time.Second), Equals, "1d")
	c.Check(FormatDuration(86401*time.Second), Equals, "1d 1s")
	c.Check(FormatDuration(86460*time.Second), Equals, "1d 1m")
	c.Check(FormatDuration(86461*time.Second), Equals, "1d 1m 1s")
	c.Check(FormatDuration(90000*time.Second), Equals, "1d 1h")
	c.Check(FormatDuration(90001*time.Second), Equals, "1d 1h 1s")
	c.Check(FormatDuration(90060*time.Second), Equals, "1d 1h 1m")
	c.Check(FormatDuration(90061*time.Second), Equals, "1d 1h 1m 1s")
	c.Check(FormatDuration(172799*time.Second), Equals, "1d 23h 59m 59s")
	c.Check(FormatDuration(172800*time.Second), Equals, "2d")
	c.Check(FormatDuration(172801*time.Second), Equals, "2d 1s")
	c.Check(FormatDuration(172860*time.Second), Equals, "2d 1m")
	c.Check(FormatDuration(172861*time.Second), Equals, "2d 1m 1s")
	c.Check(FormatDuration(259199*time.Second), Equals, "2d 23h 59m 59s")
}

func (t *Test) TestStripMarkdownLinks(c *C) {
	// only show the links
	c.Check(StripMarkdownLinks("[link](http://example.com)"), Equals, "http://example.com")
	c.Check(StripMarkdownLinks("[link](http://example.com) [link2](http://example2.com)"), Equals, "http://example.com http://example2.com")
	c.Check(StripMarkdownLinks("[link](http://example.com) [link2](http://example2.com) [link3](http://example3.com)"), Equals, "http://example.com http://example2.com http://example3.com")

	// also handle spaces in title
	c.Check(StripMarkdownLinks("[Foo Bar](http://example.com) | [Bar Baz](http://example1.com)"), Equals, "http://example.com | http://example1.com")
}

func (t *Test) TestFormatLocale(c *C) {
	c.Check(FormatLocale(1), Equals, "1")
	c.Check(FormatLocale(12), Equals, "12")
	c.Check(FormatLocale(123), Equals, "123")
	c.Check(FormatLocale(-123), Equals, "-123")
	c.Check(FormatLocale(-123.000), Equals, "-123.00000000")
	c.Check(FormatLocale(-123.123), Equals, "-123.12300000")
	c.Check(FormatLocale(1234), Equals, "1,234")
	c.Check(FormatLocale(1234.000), Equals, "1,234.00000000")
	c.Check(FormatLocale(1234.123), Equals, "1,234.12300000")
	c.Check(FormatLocale(-1234), Equals, "-1,234")
	c.Check(FormatLocale(-1234.000), Equals, "-1,234.00000000")
	c.Check(FormatLocale(-1234.123), Equals, "-1,234.12300000")
	c.Check(FormatLocale(12345), Equals, "12,345")
	c.Check(FormatLocale(123456), Equals, "123,456")
	c.Check(FormatLocale(1234567), Equals, "1,234,567")
	c.Check(FormatLocale(-1234567), Equals, "-1,234,567")
	c.Check(FormatLocale(12345678), Equals, "12,345,678")
	c.Check(FormatLocale(123456789), Equals, "123,456,789")
	c.Check(FormatLocale(1234567890), Equals, "1,234,567,890")
}
