package util

import (
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
)

////////////////////////////////////////////////////////////////////////////////////////
// Type Conversions
////////////////////////////////////////////////////////////////////////////////////////

func CoinToCommon(coin openapi.Coin) common.Coin {
	amount := cosmos.NewUintFromString(coin.Amount)
	asset, err := common.NewAsset(coin.Asset)
	if err != nil {
		log.Panic().Err(err).Str("asset", coin.Asset).Msg("failed to parse coin asset")
	}
	return common.NewCoin(asset, amount)
}

////////////////////////////////////////////////////////////////////////////////////////
// Format
////////////////////////////////////////////////////////////////////////////////////////

func FormatDuration(d time.Duration) string {
	str := ""
	days := d / (24 * time.Hour)
	if days > 0 {
		str += fmt.Sprintf("%dd ", days)
	}
	d -= days * (24 * time.Hour)
	hours := d / time.Hour
	if hours > 0 {
		str += fmt.Sprintf("%dh ", hours)
	}
	d -= hours * time.Hour
	minutes := d / time.Minute
	if minutes > 0 {
		str += fmt.Sprintf("%dm ", minutes)
	}
	d -= minutes * time.Minute
	seconds := d / time.Second
	if seconds > 0 || str == "" {
		str += fmt.Sprintf("%ds", seconds)
	}

	return strings.TrimSpace(str)
}

var reStripMarkdownLinks = regexp.MustCompile(`\[[0-9a-zA-Z_ ]+\]\((.+?)\)`)

func StripMarkdownLinks(input string) string {
	return reStripMarkdownLinks.ReplaceAllString(input, "$1")
}

func FormatLocale[T int | uint | int64 | uint64 | float64](n T) string {
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}

	// extract decimals from float
	var decimals string
	if x, ok := any(n).(float64); ok {
		n = T(int64(x))
		decimals = fmt.Sprintf("%.8f", x)[len(fmt.Sprintf("%v", n)):]
	}

	s := fmt.Sprintf("%v", n)
	if len(s) <= 3 {
		if negative {
			return "-" + s + decimals
		}
		return s + decimals
	}

	var result strings.Builder
	prefix := len(s) % 3
	if prefix > 0 {
		result.WriteString(s[:prefix])
		if len(s) > prefix {
			result.WriteByte(',')
		}
	}

	for i := prefix; i < len(s); i += 3 {
		result.WriteString(s[i : i+3])
		if i+3 < len(s) {
			result.WriteByte(',')
		}
	}

	result.WriteString(decimals)

	if negative {
		return "-" + result.String()
	}
	return result.String()
}

func FormatUSD(value float64) string {
	negative := false
	// analyze-ignore(float-comparison)
	if value < 0 {
		negative = true
		value = -value
	}
	integerPart := int(value)
	decimalPart := int((value - float64(integerPart)) * 100)
	str := fmt.Sprintf("$%s.%02d", FormatLocale(integerPart), decimalPart)
	if negative {
		str = "-" + str
	}
	return str
}

func Moneybags(usdValue uint64) string {
	count := int(usdValue / config.Get().Styles.USDPerMoneyBag)
	return strings.Repeat(config.EmojiMoneybag, count)
}

////////////////////////////////////////////////////////////////////////////////////////
// Rune Value
////////////////////////////////////////////////////////////////////////////////////////

func RuneValue(height int64, coin common.Coin) float64 {
	if coin.IsRune() {
		amt, _ := new(big.Float).Quo(
			new(big.Float).SetInt(coin.Amount.BigInt()),
			big.NewFloat(common.One),
		).Float64()
		return amt

	}

	if coin.Asset.Equals(common.TOR) {
		network := openapi.NetworkResponse{}
		err := ThornodeCachedRetryGet("thorchain/network", height, &network)
		if err != nil {
			log.Panic().Err(err).Msg("failed to get network")
		}

		price, err := strconv.ParseFloat(network.TorPriceInRune, 64)
		if err != nil {
			log.Panic().Err(err).Msg("failed to parse network rune price")
		}

		amt, _ := new(big.Float).Quo(
			new(big.Float).SetInt(coin.Amount.BigInt()),
			big.NewFloat(common.One),
		).Float64()
		pr, _ := new(big.Float).Quo(
			big.NewFloat(price),
			big.NewFloat(common.One),
		).Float64()
		return amt * pr
	}

	// get pools response
	pools := []openapi.Pool{}
	err := ThornodeCachedRetryGet("thorchain/pools", height, &pools)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get pools")
	}

	// find pool and convert value
	asset := coin.Asset.GetLayer1Asset()
	if asset.IsDerivedAsset() {
		asset.Chain = common.Chain(asset.Symbol)
	}
	for _, pool := range pools {
		if pool.Asset != asset.GetLayer1Asset().String() {
			continue
		}
		runeBalance := cosmos.NewUintFromString(pool.BalanceRune)
		assetBalance := cosmos.NewUintFromString(pool.BalanceAsset)

		runePerAsset := new(big.Float).Quo(
			new(big.Float).SetInt(runeBalance.BigInt()),
			new(big.Float).SetInt(assetBalance.BigInt()),
		)
		amountFloat := new(big.Float).Mul(
			new(big.Float).SetInt(coin.Amount.BigInt()),
			runePerAsset,
		)
		amountRuneFloat, _ := amountFloat.Quo(amountFloat, big.NewFloat(common.One)).Float64()
		return amountRuneFloat
	}

	log.Error().Str("asset", asset.String()).Msg("failed to find pool")
	return 0
}

////////////////////////////////////////////////////////////////////////////////////////
// USD Value
////////////////////////////////////////////////////////////////////////////////////////

func USDValue(height int64, coin common.Coin) float64 {
	if coin.Asset.Equals(common.TOR) {
		return float64(coin.Amount.Uint64()) / common.One
	}

	if coin.IsRune() {
		network := openapi.NetworkResponse{}
		err := ThornodeCachedRetryGet("thorchain/network", height, &network)
		if err != nil {
			log.Panic().Err(err).Msg("failed to get network")
		}

		price, err := strconv.ParseFloat(network.RunePriceInTor, 64)
		if err != nil {
			log.Panic().Err(err).Msg("failed to parse network rune price")
		}
		return float64(coin.Amount.Uint64()) / common.One * price / common.One
	}

	// get pools response
	pools := []openapi.Pool{}
	err := ThornodeCachedRetryGet("thorchain/pools", height, &pools)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get pools")
	}

	// find pool and convert value
	asset := coin.Asset.GetLayer1Asset()
	if asset.IsDerivedAsset() {
		asset.Chain = common.Chain(asset.Symbol)
	}
	for _, pool := range pools {
		if pool.Asset != asset.GetLayer1Asset().String() {
			continue
		}
		if pool.AssetTorPrice == "" {
			return 0
		}
		price := cosmos.NewUintFromString(pool.AssetTorPrice)
		return float64(coin.Amount.Uint64()) / common.One * float64(price.Uint64()) / common.One
	}

	log.Error().Str("asset", asset.String()).Msg("failed to find pool")
	return 0
}

func ExternalUSDValue(coin common.Coin) float64 {
	// parameters for crypto compare api
	fsym := ""

	switch coin.Asset.GetLayer1Asset() {
	case common.BTCAsset:
		fsym = "BTC"
	case common.ETHAsset:
		fsym = "ETH"
	default:
		log.Error().Str("asset", coin.Asset.String()).Msg("unsupported external value asset")
		return 0
	}

	// get price from crypto compare
	tsyms := "USD"
	url := fmt.Sprintf("https://min-api.cryptocompare.com/data/price?fsym=%s&tsyms=%s", fsym, tsyms)
	price := struct {
		USD float64 `json:"USD"`
	}{}
	err := RetryGet(url, &price)
	if err != nil {
		log.Error().Err(err).Str("url", url).Msg("failed to get external price")
		return 0
	}

	return float64(coin.Amount.Uint64()) * price.USD / common.One
}

func USDValueString(height int64, coin common.Coin) string {
	value := USDValue(height, coin)
	return FormatUSD(value)
}

////////////////////////////////////////////////////////////////////////////////////////
// Clout
////////////////////////////////////////////////////////////////////////////////////////

func Clout(height int64, address string) common.Coin {
	cloutScore := cosmos.ZeroUint()

	// retrieve address clout
	clout := openapi.SwapperCloutResponse{}
	err := ThornodeCachedRetryGet("thorchain/clout/swap/"+address, height, &clout, http.StatusNotFound)
	if err != nil {
		log.Error().Err(err).
			Str("address", address).
			Int64("height", height).
			Msg("failed to get clout")
	}
	if clout.Score != nil {
		cloutScore = cosmos.NewUintFromString(*clout.Score)
	}

	return common.NewCoin(common.RuneNative, cloutScore)
}

////////////////////////////////////////////////////////////////////////////////////////
// Address Checks
////////////////////////////////////////////////////////////////////////////////////////

func IsThorchainModule(address string) bool {
	thorchainModulesAddresses := map[string]bool{
		"thor1v8ppstuf6e3x0r4glqc68d5jqcs2tf38cg2q6y":  true,
		"sthor1v8ppstuf6e3x0r4glqc68d5jqcs2tf38v3kkv6": true,
	}
	return thorchainModulesAddresses[address]
}

////////////////////////////////////////////////////////////////////////////////////////
// Config
////////////////////////////////////////////////////////////////////////////////////////

// GetConfigInt64 mirrors the thornode GetConfigInt64 function to first lookup the
// corresponding mimir value, and fallback to the constant value.
func GetConfigInt64(height int64, key constants.ConstantName) int64 {
	mimir := map[string]int64{}
	err := ThornodeCachedRetryGet("thorchain/mimir", height, &mimir)
	if err != nil {
		log.Panic().Err(err).Msg("failed to get mimirs")
	}
	value, ok := mimir[strings.ToUpper(key.String())]
	if ok {
		return value
	}
	return constants.NewConstantValue().GetInt64Value(key)
}
