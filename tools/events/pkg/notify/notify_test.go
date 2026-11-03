package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
)

func TestPackage(t *testing.T) { TestingT(t) }

type NotifySuite struct{}

var _ = Suite(&NotifySuite{})

func (s *NotifySuite) TestSlackSuccess(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data map[string]string
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(strings.Contains(data["text"], "test title"), Equals, true)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := slack(server.URL, "test title", []string{"line1", "line2"}, Info, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestSlackWithDangerLevel(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data map[string]string
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(strings.Contains(data["text"], "<!here>"), Equals, true)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := slack(server.URL, "alert", []string{}, Danger, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestSlackWithFields(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data map[string]string
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(strings.Contains(data["text"], "*Key*: value"), Equals, true)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	fields := util.NewOrderedMap()
	fields.Set("Key", "value")

	err := slack(server.URL, "", []string{}, Info, fields)
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestSlackServerError(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("error"))
	}))
	defer server.Close()

	err := slack(server.URL, "test", []string{}, Info, util.NewOrderedMap())
	c.Assert(err, NotNil)
}

func (s *NotifySuite) TestSlackBadURL(c *C) {
	err := slack("http://invalid-url-that-doesnt-exist:99999", "test", []string{}, Info, util.NewOrderedMap())
	c.Assert(err, NotNil)
}

func (s *NotifySuite) TestSlackMarkdownLinks(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data map[string]string
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		// Markdown [text](url) should be converted to Slack <url|text>
		c.Assert(strings.Contains(data["text"], "<https://example.com|click here>"), Equals, true)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := slack(server.URL, "", []string{"[click here](https://example.com)"}, Info, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordSuccess(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(data.Embeds, HasLen, 1)
		c.Assert(strings.Contains(data.Embeds[0].Description, "Test Title"), Equals, true)
		c.Assert(data.Embeds[0].Color, Equals, 0x4381FD) // Info = blue

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := discord(server.URL, "Test Title", 12345, []string{"line1"}, Info, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordBroadcastLevel(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(data.Content, Equals, "@here")
		c.Assert(data.Embeds[0].Color, Equals, 0x4381FD) // Broadcast = blue

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := discord(server.URL, "alert", 1, nil, Broadcast, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordDangerLevel(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(data.Content, Equals, "@here")
		c.Assert(data.Embeds[0].Color, Equals, 0xFF5B60) // Danger = red

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := discord(server.URL, "danger", 1, nil, Danger, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordSuccessLevel(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(data.Embeds[0].Color, Equals, 0x4ACC4C) // Success = green

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := discord(server.URL, "ok", 1, nil, Success, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordWarningLevel(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(data.Embeds[0].Color, Equals, 0xFFF674) // Warning = yellow

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := discord(server.URL, "warn", 1, nil, Warning, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordWithFields(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(data.Embeds[0].Fields, HasLen, 1)
		c.Assert(data.Embeds[0].Fields[0].Name, Equals, "Amount")
		c.Assert(data.Embeds[0].Fields[0].Value, Equals, "$100")

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	fields := util.NewOrderedMap()
	fields.Set("Amount", "$100")

	err := discord(server.URL, "test", 1, nil, Info, fields)
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordEmptyFieldSkipped(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		c.Assert(data.Embeds[0].Fields, HasLen, 0)

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	fields := util.NewOrderedMap()
	fields.Set("Empty", "")

	err := discord(server.URL, "test", 1, nil, Info, fields)
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestDiscordServerError(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("error"))
	}))
	defer server.Close()

	err := discord(server.URL, "test", 1, nil, Info, util.NewOrderedMap())
	c.Assert(err, NotNil)
}

func (s *NotifySuite) TestDiscordBadURL(c *C) {
	err := discord("http://invalid-url-that-doesnt-exist:99999", "test", 1, nil, Info, util.NewOrderedMap())
	c.Assert(err, NotNil)
}

func (s *NotifySuite) TestDiscordTitleWithBlock(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		c.Assert(err, IsNil)
		defer r.Body.Close()

		var data DiscordMessage
		err = json.Unmarshal(body, &data)
		c.Assert(err, IsNil)
		// When title matches block pattern, URL should be set
		c.Assert(strings.Contains(data.Embeds[0].URL, "thorchain/block?height=42"), Equals, true)

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := discord(server.URL, "`[42]` Test", 42, nil, Info, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestPagerduty(c *C) {
	err := pagerduty("http://example.com", "test", []string{"line"}, util.NewOrderedMap())
	c.Assert(err, IsNil)
}

func (s *NotifySuite) TestConsole(c *C) {
	// console just prints to stdout, make sure it doesn't panic
	fields := util.NewOrderedMap()
	fields.Set("Key", "value")

	console("Test", "**bold** `code` _italic_", []string{"line with [link](https://example.com)"}, Info, fields)
	console("Alert", "title", []string{}, Danger, nil)
	console("Category", "", []string{"no title"}, Warning, util.NewOrderedMap())
}

func (s *NotifySuite) TestNonMainnetQueryParams(c *C) {
	// Default config has network = "mainnet", so no params should be added
	cfg := config.Get()
	if cfg.Network == "mainnet" {
		msg := "https://runescan.io/tx/abc123"
		result := nonMainnetQueryParams(msg)
		c.Assert(result, Equals, msg) // no modification for mainnet
	}
}

func (s *NotifySuite) TestNotifyConsoleMode(c *C) {
	// When no webhooks are set and console is false, Notify should be a no-op
	w := config.Webhooks{
		Category: "Test",
	}
	fields := util.NewOrderedMap()

	// This should not panic (no webhooks to send to, no console mode)
	Notify(w, "test title", 100, []string{"line"}, Info, fields)
}
