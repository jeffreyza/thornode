package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
)

////////////////////////////////////////////////////////////////////////////////////////
// Notify
////////////////////////////////////////////////////////////////////////////////////////

func Notify(w config.Webhooks, title string, block int64, lines []string, level Level, fields *util.OrderedMap) {
	discordTitle := title
	title = fmt.Sprintf("`[%d]` %s", block, title)

	log.Info().Str("title", title).Msg("sending notifications")

	// if in console mode only print
	if config.Get().Console {
		console(w.Category, title, lines, level, fields)
	}

	// copy lines to avoid modifying the original slice
	linesCopy := append([]string{}, lines...)

	// send slack
	if w.Slack != "" {
		err := util.Retry(
			config.Get().MaxRetries,
			func() error { return slack(w.Slack, title, linesCopy, level, fields) },
		)
		if err != nil {
			log.Panic().Err(err).Msg("unable to send slack notification")
		}
	}

	// send discord
	copy(linesCopy, lines)
	if w.Discord != "" {
		err := util.Retry(
			config.Get().MaxRetries,
			func() error { return discord(w.Discord, discordTitle, block, linesCopy, level, fields) },
		)
		if err != nil {
			log.Panic().Err(err).Msg("unable to send discord notification")
		}
	}

	// send pagerduty
	copy(linesCopy, lines)
	if w.PagerDuty != "" {
		err := util.Retry(
			config.Get().MaxRetries,
			func() error { return pagerduty(w.PagerDuty, title, linesCopy, fields) },
		)
		if err != nil {
			log.Panic().Err(err).Msg("unable to send pagerduty notification")
		}
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Internal
////////////////////////////////////////////////////////////////////////////////////////

// match markdown links
var reLinkMdToSlack = regexp.MustCompile(`\[(.*?)\]\((.*?)\)`)

func slack(webhook, title string, lines []string, level Level, fields *util.OrderedMap) error {
	if title != "" {
		lines = append([]string{fmt.Sprintf("*%s*", title)}, lines...)
	}

	// add fields to the message
	for _, k := range fields.Keys() {
		v, _ := fields.Get(k)
		lines = append(lines, fmt.Sprintf("*%s*: %s", k, v))
	}

	// add tags to the message
	if level == Danger {
		lines = append(lines, "<!here>")
	}

	// format lines of the message as a quote
	for i, line := range lines {
		lines[i] = "> " + line
	}

	// join the lines into a single message
	message := strings.Join(lines, "\n")

	// add network params
	message = nonMainnetQueryParams(message)

	// replace markdown links with slack links
	message = reLinkMdToSlack.ReplaceAllString(message, "<$2|$1>")

	// map bold formatting to slack version
	message = strings.ReplaceAll(message, "**", "*")

	// build the request
	data := map[string]string{
		"text": message,
	}
	body, err := json.Marshal(data)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal slack message")
		return err
	}

	// send the request
	resp, err := http.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Error().Err(err).Msg("unable to send slack message")
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, err = io.ReadAll(resp.Body)
		if err == nil {
			log.Error().Str("status", resp.Status).Str("body", string(body)).Msg("slack error")
		} else {
			log.Error().Err(err).Str("status", resp.Status).Msg("unable to read slack response")
		}
		return fmt.Errorf("failed to send slack message")
	}

	return nil
}

func console(category, title string, lines []string, level Level, fields *util.OrderedMap) {
	// ansi escape codes
	boldStart := "\033[1m"
	italicStart := "\033[3m"
	blue := "\033[34m"
	reset := "\033[0m"

	// add alert tag for @here tags in console mode
	if level == Danger {
		category = "🚨 " + category + " 🚨"
	}

	if title != "" {
		lines = append([]string{fmt.Sprintf("%s%s%s", boldStart, title, reset)}, lines...)
	}

	// add fields to the message
	if fields != nil {
		for _, k := range fields.Keys() {
			v, _ := fields.Get(k)
			lines = append(lines, fmt.Sprintf("%s%s%s: %s", italicStart, k, reset, v))
		}
	}

	fmt.Println()
	fmt.Printf("------------------------- %s -------------------------\n", category)
	for _, line := range lines {
		// strip markdown line formatting
		line = util.StripMarkdownLinks(line)

		// add network params
		line = nonMainnetQueryParams(line)

		// replace emojis
		line = strings.ReplaceAll(line, config.EmojiMoneybag, "💰")
		line = strings.ReplaceAll(line, config.EmojiMoneyWithWings, "💸")
		line = strings.ReplaceAll(line, config.EmojiDollar, "💵")
		line = strings.ReplaceAll(line, config.EmojiWhiteCheckMark, "✅")
		line = strings.ReplaceAll(line, config.EmojiSmallRedTriangle, "🔺")
		line = strings.ReplaceAll(line, config.EmojiRotatingLight, "🚨")

		// handle ansi formatting
		for {
			newLine := strings.Replace(line, "**", boldStart, 1)
			newLine = strings.Replace(newLine, "**", reset, 1)
			newLine = strings.Replace(newLine, "`", blue, 1)
			newLine = strings.Replace(newLine, "`", reset, 1)
			newLine = strings.Replace(newLine, "_", italicStart, 1)
			newLine = strings.Replace(newLine, "_", reset, 1)
			if newLine == line {
				break
			}
			line = newLine
		}

		fmt.Println(line)
	}
	fmt.Println("--------------------------------------------------")
	fmt.Println()
}

func pagerduty(webhook, title string, lines []string, fields *util.OrderedMap) error {
	log.Error().Msg("pagerduty not yet implemented")
	return nil
}

// nonMainnetQueryParams adds ?network=<network> to explorer and tracker links for non-mainnet networks.
func nonMainnetQueryParams(msg string) string {
	network := config.Get().Network
	if network != "" && network != "mainnet" {
		reExplorer := regexp.MustCompile(fmt.Sprintf(`%s[^\s()]+`, config.Get().Links.Explorer))
		reTracker := regexp.MustCompile(fmt.Sprintf(`%s[^\s()]+`, config.Get().Links.Track))

		msg = reExplorer.ReplaceAllString(msg, fmt.Sprintf("$0?network=%s", network))
		msg = reTracker.ReplaceAllString(msg, fmt.Sprintf("$0?network=%s", network))
	}
	return msg
}
