package util

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	lru "github.com/hashicorp/golang-lru"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
)

////////////////////////////////////////////////////////////////////////////////////////
// Retry
////////////////////////////////////////////////////////////////////////////////////////

func RetryGet(url string, result interface{}) error {
	return Retry(config.Get().MaxRetries, func() error {
		// make the request
		res, err := http.DefaultClient.Get(url)
		if err != nil {
			return err
		}

		// check the status code
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: status code %d", url, res.StatusCode)
		}

		// populate the result
		defer res.Body.Close()
		return json.NewDecoder(res.Body).Decode(result)
	})
}

////////////////////////////////////////////////////////////////////////////////////////
// Cache
////////////////////////////////////////////////////////////////////////////////////////

var cache *lru.Cache

func InitCache() {
	var err error
	cache, err = lru.New(config.Get().Endpoints.CacheSize)
	if err != nil {
		log.Panic().Err(err).Msg("failed to initialize cache")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Cache
////////////////////////////////////////////////////////////////////////////////////////

// ThornodeCachedRetryGet fetches the Thornode API response at the provided height with
// retry. Responses at a specific height are assumed immutable and cached indefinitely.
func ThornodeCachedRetryGet(path string, height int64, result interface{}, allowStatus ...int) error {
	url := fmt.Sprintf("%s/%s?height=%d", config.Get().Endpoints.Thornode, path, height)

	// check the cache first
	if val, ok := cache.Get(url); ok {
		var bytes []byte
		bytes, ok = val.([]byte)
		if !ok {
			log.Panic().Msg("unreachable: failed to cast cache value to []byte")
		}
		if len(bytes) > 0 {
			return json.Unmarshal(bytes, result)
		}
	}

	var body []byte

	// attempt to populate the cache
	err := Retry(config.Get().MaxRetries, func() error {
		// create the request and self-identify
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-Client-ID", "events")

		// make the request
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}

		// check the status code
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("status code %d", res.StatusCode)
		}

		// populate the cache
		defer res.Body.Close()
		body, err = io.ReadAll(res.Body)
		if err != nil {
			return err
		}

		cache.Add(url, body)

		return nil
	})
	if err != nil {
		return err
	}

	return json.Unmarshal(body, result)
}
