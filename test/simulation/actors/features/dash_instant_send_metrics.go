package features

import (
	"bufio"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type instantSendMetrics struct {
	available float64 // duffs
	warmingUp bool
}

// readInstantSendMetrics reads the Bifrost instance used by this simulation.
func readInstantSendMetrics() (instantSendMetrics, error) {
	return scrapeInstantSendMetrics("http://localhost:9000/metrics")
}

// scrapeInstantSendMetrics requires the allowance and warm-up gauges from a successful scrape.
// Missing or invalid samples must not look like a ready allowance.
func scrapeInstantSendMetrics(endpoint string) (instantSendMetrics, error) {
	var state instantSendMetrics
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return state, fmt.Errorf("Bifrost metrics returned HTTP %d", resp.StatusCode)
	}
	var warmingUp float64
	values := map[string]*float64{
		"dash_instantsend_available_duffs": &state.available,
		"dash_instantsend_warming_up":      &warmingUp,
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 {
			continue
		}
		target, ok := values[parts[0]]
		if !ok {
			continue
		}
		if len(parts) != 2 {
			return state, fmt.Errorf("malformed InstantSend metric %s", parts[0])
		}
		var value float64
		value, err = strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return state, fmt.Errorf("parse InstantSend metric %s: %w", parts[0], err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return state, fmt.Errorf("invalid InstantSend metric %s: %s", parts[0], parts[1])
		}
		*target = value
		delete(values, parts[0])
	}
	if err = scanner.Err(); err != nil {
		return state, err
	}
	for name := range values {
		return state, fmt.Errorf("missing InstantSend metric %s", name)
	}
	switch warmingUp {
	case 0:
	case 1:
		state.warmingUp = true
	default:
		return state, fmt.Errorf("invalid InstantSend warm-up status: %g", warmingUp)
	}
	return state, nil
}
