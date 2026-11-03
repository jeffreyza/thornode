package util

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

////////////////////////////////////////////////////////////////////////////////////////
// OrderedMap
////////////////////////////////////////////////////////////////////////////////////////

type OrderedMap struct {
	keys   []string
	values map[string]interface{}
}

func NewOrderedMap() *OrderedMap {
	return &OrderedMap{
		keys:   []string{},
		values: make(map[string]interface{}),
	}
}

func (om *OrderedMap) Set(key string, value interface{}) {
	if _, ok := om.values[key]; !ok {
		om.keys = append(om.keys, key)
	}
	om.values[key] = value
}

func (om *OrderedMap) Get(key string) (interface{}, bool) {
	value, ok := om.values[key]
	return value, ok
}

func (om *OrderedMap) Delete(key string) {
	if _, ok := om.values[key]; ok {
		delete(om.values, key)
		for i, k := range om.keys {
			if k == key {
				om.keys = append(om.keys[:i], om.keys[i+1:]...)
				break
			}
		}
	}
}

func (om *OrderedMap) Keys() []string {
	if om == nil {
		return []string{}
	}
	return om.keys
}

func (om *OrderedMap) String() string {
	return fmt.Sprintf("%+v", om.values)
}

////////////////////////////////////////////////////////////////////////////////////////
// Midgard
////////////////////////////////////////////////////////////////////////////////////////

// MidgardActionsResponse contains a subset of fields we use from Midgard actions.
type MidgardActionsResponse struct {
	Actions []struct {
		Type     string `json:"type"`
		Metadata struct {
			Swap struct {
				LiquidityFee string `json:"liquidityFee"`
				NetworkFees  []struct {
					Asset  common.Asset `json:"asset"`
					Amount cosmos.Uint  `json:"amount"`
				}
			} `json:"swap"`
		} `json:"metadata"`
	} `json:"actions"`
}
