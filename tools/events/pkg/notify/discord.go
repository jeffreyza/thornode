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

func discord(webhook, title string, block int64, lines []string, level Level, fields *util.OrderedMap) error {
	embed := DiscordEmbed{
		Description: fmt.Sprintf(
			"**%s** at [block `%d`](%s/thorchain/block?height=%d)",
			title, block, config.Get().Links.Thornode, block,
		),
	}
	if len(lines) > 0 {
		embed.Description += "\n\n" + strings.Join(lines, "\n")
	}

	// extract block from title and add link to block response
	reBlock := regexp.MustCompile("^`\\[([0-9]+)\\]`")
	if reBlock.MatchString(title) {
		block := reBlock.FindStringSubmatch(title)[1]
		embed.URL = fmt.Sprintf("%s/thorchain/block?height=%s", config.Get().Links.Thornode, block)
	}

	// add fields to the message
	for _, k := range fields.Keys() {
		v, _ := fields.Get(k)
		vString, _ := v.(string)

		// skip empty fields
		if vString == "" {
			continue
		}

		// add network params to any urls
		vString = nonMainnetQueryParams(vString)

		embed.Fields = append(embed.Fields, DiscordEmbedField{
			Name:   k,
			Value:  vString,
			Inline: len(vString) < 25 && len(k) < 25,
		})
	}

	// add tags to the message and set color based on level
	switch level {
	case Info, Broadcast:
		embed.Color = 0x4381FD // blue
	case Success:
		embed.Color = 0x4ACC4C // green
	case Warning:
		embed.Color = 0xFFF674 // yellow
	case Error, Danger:
		embed.Color = 0xFF5B60 // red
	}

	// build the request
	data := DiscordMessage{
		Embeds: []DiscordEmbed{embed},
	}
	if level == Broadcast || level == Danger { // @here tag for broadcast or danger
		data.Content = "@here"
	}
	body, err := json.Marshal(data)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal discord message")
		return err
	}

	// send the request
	resp, err := http.Post(webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Error().Err(err).Msg("unable to send discord message")
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, err = io.ReadAll(resp.Body)
		if err == nil {
			log.Error().Str("status", resp.Status).Str("body", string(body)).Msg("discord error")
		} else {
			log.Error().Err(err).Str("status", resp.Status).Msg("unable to read discord response")
		}
		return fmt.Errorf("failed to send discord message")
	}

	return nil
}
