package watchers

import (
	"net"
	"net/http"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/config"
)

var thornodeURL string

func init() {
	config.Init()
	thornodeURL = config.GetBifrost().Thorchain.ChainHost
	if !strings.HasPrefix(thornodeURL, "http") {
		thornodeURL = "http://" + thornodeURL
	}
}

var httpClient = &http.Client{
	Transport: &http.Transport{
		Dial: (&net.Dialer{
			Timeout: 5 * time.Second,
		}).Dial,
	},
	Timeout: 5 * time.Second,
}
