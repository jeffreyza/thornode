//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// --- Step 7: SOL phantom-insolvency slash refund (block 27,591,961) ---
//
// A stuck SOL outbound (broadcast on-chain but missing observation quorum) tripped the
// solvency check at height 27,591,961 and bond-slashed all 19 members of the ...AV37HT-SOL
// Asgard vault. The insolvency slash (manager_slasher) took 12,281.70332541 RUNE from bond
// and split it Bond->Reserve (half) + Bond->Asgard with pool.BalanceRune += the other half
// (6,140.85166271 RUNE credited to the SOL pool), and separately marked the SOL pool asset
// down by 30.64000109 SOL (the amount the vault was presumed to have lost). Observations
// were later collected and solvency reconciled, but the slash stands.
//
// Chain-verified at the block: Sigma per-provider bond delta 27,591,960->961 ==
// 1228170332541 (exact), per-node deltas reconcile, SOL pool BalanceRune +614085166271 and
// BalanceAsset -3064000109 (30.64 SOL).
//
// This step restores each bond provider by exactly the RUNE deducted from its bond, funded
// Reserve->Bond, mirroring how it was slashed (not a lump operator credit). Per-node
// best-effort: a node whose account/providers cannot be read, or whose Reserve->Bond funding
// fails, is logged and skipped so drifted state cannot abort the whole store migration.
//
// Pool leg (solSlashPoolLeg) is Itz's call -- a one-line switch:
//   - poolLegNone (A):         leave the pool's phantom RUNE credit; Reserve funds the full
//                              bond restore (Reserve nets -6,140.85, pool keeps +6,140.85).
//   - poolLegReclaimRune (C):  reclaim the 6,140.85 phantom RUNE from the SOL pool back to
//                              the Reserve (pool RUNE -> pre-slash), so the restore nets out.
//   - poolLegFullReversal (B): ReclaimRune + also restore the 30.64 SOL pool-asset markdown.
//                              Backed only if the vault still holds that unaccounted SOL
//                              (observation was collected post-slash, so it likely does);
//                              if chosen, solvency-gate the SOL restore like the ZEC/TRON
//                              legs. NOT IMPLEMENTED until Itz picks it.
//
// HaltSigning NOTE: the Reserve->Bond leg does NOT gate on HaltSigning<chain> -- it has no
// vault/pool credit an unobserved signing could double-spend (the phantom insolvency was a
// MISSED observation, not a double-pay); its guard is the per-node best-effort soft-skip plus
// the pinned, chain-reconciled amounts. The poolLegFullReversal SOL-asset credit, by contrast,
// DOES gate on HaltSigningSOL + the unaccounted-vault solvency check, exactly like the
// ZEC/TRON pool-asset refunds: it re-credits pool depth backed by SOL the vault still holds,
// which only stays backed while SOL signing is halted (set before the upgrade, launch plan).

type solSlashPoolLegMode int

const (
	poolLegNone solSlashPoolLegMode = iota
	poolLegReclaimRune
	poolLegFullReversal
)

// solSlashPoolLeg selects how the SOL pool legs of the phantom slash are reversed. Itz chose
// B (poolLegFullReversal, board 655): a COMPLETE reversal, BTC/ZEC/TRON/DOGE parity --
// Reserve->Bond refund PLUS reverse the pool leg (credit the 30.64 SOL markdown back to the
// pool and debit the 6,140.85 phantom slash RUNE). The SOL-asset credit is solvency-gated on
// the vault's unaccounted SOL AND HaltSigningSOL-gated, exactly like the ZEC/TRON pool-asset
// refunds. poolLegNone (A) / poolLegReclaimRune (C) remain as a one-line switch.
const solSlashPoolLeg = poolLegFullReversal

const (
	solSlashBondRefundTotal   = uint64(1_228_170_332_541) // 12,281.70332541 RUNE (Sigma of the 208 rows)
	solSlashPoolRuneCredit    = uint64(614_085_166_271)   // phantom RUNE the slash credited to the SOL pool (chain-verified)
	solSlashPoolAssetMarkdown = uint64(3_064_000_109)     // 30.64000109 SOL markdown (poolLegFullReversal only)
	solSlashAsset             = "SOL.SOL"
)

// mainnetSolSlashRefunds19to20 -- 208 rows / 19 nodes / 176 providers; each {node, provider,
// exact RUNE (1e8) deducted from that provider bond at 27,591,961}. Grouped by node (sorted).
// Sum = solSlashBondRefundTotal. Re-verify at the release cut.
var mainnetSolSlashRefunds19to20 = []struct {
	node     string
	provider string
	amount   uint64
}{
	{"thor159kkv6wf74teuxkm0vd4ude8eu9sucjytl63d0", "thor142s5raq6jtes359hmy8jte9r34lj8gqs5dx5uk", 73663590253},
	{"thor159kkv6wf74teuxkm0vd4ude8eu9sucjytl63d0", "thor1uwpshu52te8n36mk8hfy3ejjr64jma2s6wsmfq", 386724},
	{"thor18x0naax76g7ylp7np3wwtthulw55ngtt5w2rcn", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 10378952333},
	{"thor18x0naax76g7ylp7np3wwtthulw55ngtt5w2rcn", "thor1jnnep660vqjkkyjxguw84sstvawew25cs6dw5x", 48685733845},
	{"thor18x0naax76g7ylp7np3wwtthulw55ngtt5w2rcn", "thor1mpdccj4wr88v2jw28cw2cqluqqcrkux6n45n2m", 700233},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor12zg77pdpluzyu9cnj429r6etrk25myk2xlzxn5", 8124376771},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor13hapk9r6r5gvddtvnt6uu8w0aymjuq7pnq49wq", 8012631167},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor14n5k9vw259xue689evpp5h7vlmwz9r4j6vxz2m", 6138670372},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor169wy974lr2c8gsesgv4jjf7z6czgge5pfa75l6", 1281096},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor19upsdjennvn7fp3r6dc06gx29gh3ut6nxu6qkn", 723450278},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor1apk9rtnhvzs8p55426gvm3f688jzz2gff8czfu", 49889131693},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor1ay9fa7rp8nsh63qnjzh2fdft93643258usp29r", 2302127893},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor1d87pflneuugjd9wr0m6anl9tkyqc0xrwhx334x", 602098721},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor1j37a9k5uezn7d3965kw0qnj3da6jh9q0qwvxhu", 27856214},
	{"thor19dxkzzp09egc06wv4jcg5q2l6zy2yg3yyxplcw", "thor1nydv7ut2advcsr7sfqj7zt77nk6f2h8zyp87r8", 6794},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor13nsa9gc8svlnqpxdns64dwq5zlvhhhwq8j3cus", 38339},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor14wuvjehqkqm34hjtvfm5asgzu94f8yy592rl9z", 350669783},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor15d7x57v7gu3j3kgp2pfejeaf9h37vj4cyelrtg", 3519961604},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor15lujyurhul92l7ykapttw5zgvv3xhy5nuv3es6", 229031419},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor16r0rpgnnk8y32z8qqhz83kz3m9uqvqwdrqfj8k", 8328891856},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor179h5c5qxhguyw9afv0ml6e9fjve3ww05ml35z5", 354288130},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1cjxuk8434fa2mj55lattaswrhj6shnk809fyus", 2502805718},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1ct2dwvj52gamnyaga902fk4r7lvu5u23ll6vz0", 1},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 10313819314},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1ezk4udl9e3hr80lahytds8dwgf0jqregccm9nf", 1740514949},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1lwrr45wjp7tf4vwsy4m7h0th4rampz6kl2l3vd", 5917486191},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1mzpdkvznuemjetfuna4g83y27du3cdgkfgn2n0", 20472414893},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1nhv5caxlj25sa5j46pp0ltc4zvwu7jgxyufywf", 2276941},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1p29xlls55nwffmmhses53z4f5aahpdwtancstt", 20961040},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1ppvm2aykc7zl7uvn3zrt2ly64p0klxf8dz3uxq", 2118295007},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1u09fgavvwxm9phhafvtkmtsmc3h8jf4cm4vhct", 183},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1u2pra86ra7ylv6k9cesrspsrv05ggkkezmuc6s", 118},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1unrtm2agxlkrv6jfzse469petlqty99y9tfdmg", 690956088},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1ww6vaxzhzpvstltphtjf6x0x3m092axkpd2gyx", 3107333144},
	{"thor1a7ut4mcte44aq8mun6lhj3rh73j95m08hfdvsr", "thor1yy6uc2rgqknl9ugqqgvlur0v2the46xhw863fq", 1443848185},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor17dhu5eceyenzv2vvyvaas6h93tuzm5azdgh3e9", 1778324429},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor18zg6y8ylus8n3tpzu5xxge3yyquj03vylstrl3", 22213470848},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1e2xrqpxuy2qxg2eeukqahlkwxrjjlkewyqlwp2", 11950},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1hxrydan0eypp5eutah2dt63swnqwzuvc0elgmd", 28026534790},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1n3dc8j6x4762jkhw39gtqx8exx59p6huld9u43", 2611005567},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1qk52yas6gty2z6js7y564geh3pqq324ht08nzk", 6943895868},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1s37zclh4rg0aguyy26sxqh5qn8zlj7yhvedvys", 674},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1tfm4q8u57qzsznpvh02s8j483aga63cl02k6jt", 810116},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1vwuyfwk85a247ett40j7uwk3k0s8cm5qaxevwl", 15417927440},
	{"thor1agftrgu74z84hef6dt6ykhe7cmjf3f8dcpkfun", "thor1wmrse405fdq8yk3xa5alxnfw6yjndk3mvn87dp", 121986},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor10cxwtu5z3vwkl54uypas0sgusvzmmtmrmqw9pm", 1218806682},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor10guhjpr9w3u5kq2nxuzrqm47d9yrm60kerf99q", 670837711},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor120vexx8hq06d5nc38e5up9m92jc6hflaum6dfu", 772991760},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor12pauwymcs9jsa0xxk6gdj2yycclqluqkve49q5", 1936159546},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor12pcpv29fmj3rqqjefy9eu6q080xhr6f37edmxf", 72853435},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor12swpcch3nuc8g3mz00v57fd8ys2uph6s0lzvz2", 1460181323},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor13nsa9gc8svlnqpxdns64dwq5zlvhhhwq8j3cus", 2107854140},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor16vhalust5987hltgm2dpv843t5r5ctqekm8dlf", 215278934},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor17g3u6jh0tp5ey42mfq500ek22nn43g6kn7wprd", 82335056},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor17vngul7c990n0umzj8qjjf5exng6xxpte0pqx8", 848096168},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1agzav5292wy7apzl7lywhdna858dgdd63ult2w", 344909874},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1c0huztdqx5y39mdpz5x9uy5cfga0k3ghd0we2y", 2880056490},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1cqtk4gy47rq265dnjh8zzjnkl7jfmvu8w3p09q", 1823003289},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1dk9y6ys5eqrcnut9z3ygjsa4al6flvcgxl8x2l", 191588501},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1e0hj0p6trsrw8v5dnfjgmsgeu77mnk34qkgfur", 2869878969},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 2836392912},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1eqrnryd8txp434xsudyjykj6y6u49ck55s47mk", 7934679211},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1fkd9rm6fkehrk3phupq7c2vsef0xqv448j7j9y", 1070557619},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1hj3ts525ehk9e64thckl89lc5pfw6g0p6kh6uc", 720206519},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1jnnep660vqjkkyjxguw84sstvawew25cs6dw5x", 11746101032},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1k43xjy4s98d6kaw2q9p2xljgeqnqvquqrse5wf", 1464580911},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1mesm8v6w6p4x4j5qyrdmn02y0qe5uy9h52hl76", 1413640508},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1mx4vxr2wc404mxmqdnugl7hkt2xx576q2t4j60", 1693213873},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1p8y207hu3r6rz56u4urecdx6mcg6q6dvnthamy", 3971727538},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1q0zjmlacrm44h0ru5t8t0jn2lm7vfy4x6fznem", 397801612},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1qjqw6gn72eprm7lsvh45257e7ppcggk5gx3zyv", 4534393975},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1r4vjqhkwhhy25kjy7y96lkl4gcrm4u5tsp5894", 804540726},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1sun0lz9a295lt4sxwx4ptz7u5jpal2e6eepyt0", 204648648},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1tumaj3y4l6q425d8kg0yjppljyuxv5prp2rtjs", 370367865},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1u4nel2t7q50mcteugkpvvm6kx8gyyvsqx4sk3t", 1612744720},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1upuw8yvg96uf4nd6jfuq9s25284dzr45kyzarc", 1398034},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1uzq9cvjrpvmky2d04ku3xplpz9p276904g3rhc", 9133551107},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1wu42unqwrs6f9743fr06ax6xt55es62y6wmwe3", 4032092516},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1x5c4pyr2d0te563rjpyp7lxhdl2ze5whfmz0q9", 9120948580},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1xq4fqrehxn7507rsaj65gd7tqp78ta2kzc9rjg", 3138453639},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1ys4eqwefvhj0n5ycs83gjuvnjup3vwscrm5u7l", 730844055},
	{"thor1d7rerzkzus2t6v2yv9wuw9xg8a8aqse8p6nvm8", "thor1z5y0ufnv356f3mhs42twek96ec7l2pdp63vsxg", 4327212536},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor13nsa9gc8svlnqpxdns64dwq5zlvhhhwq8j3cus", 2045527704},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor145j86em232hwuh7h483eqe5shamnwp2wwqsg5j", 30686517318},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor15claxhk2ca6t55yqyy5rsa7yewgm94rwdlzutu", 75002754},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor15lc7tedul9ccf66tajn6yx7n6swszy68awptt5", 5454744616},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor17fvsxqvz4u2wxw3rarayej58c84zch4aym2fkn", 213945618},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor17vgwht4wzs0juyqg4g9r9t4gcyjam3zezl6gfd", 1546761067},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1dj7r0pv4hd0uwtkjy0mq2lm8tw4xwkyaey20s9", 238645077},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 20013506},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1h70z0n8u3tejuelmx5hvg2xtw8dfqa4ve3eufp", 204552983},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1m24nuaxr5g42qec3mc73ucdk7zwf9qsrmkjhx6", 5629972964},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1pxh55qj7xfkayesj4kneqrrwlkf000f8pmkyn3", 223644536},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1qmatyjpgpws9pdt04c8z0g4appdyvhg7egj6qs", 367513071},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1s37zclh4rg0aguyy26sxqh5qn8zlj7yhvedvys", 237076744},
	{"thor1k8lchd5f88sus0fcjnujuqad8rhe8wl6yzc3aj", "thor1unrtm2agxlkrv6jfzse469petlqty99y9tfdmg", 68184243},
	{"thor1kchgh8t790zlfatdun975mu04xvumq3qjms65a", "thor12sj00sl4mv2ht260kll4tqcj6afqymzvs97u90", 10837051474},
	{"thor1kchgh8t790zlfatdun975mu04xvumq3qjms65a", "thor17n9l8sgqcnq3r2cl6wy2dsrcyutusyy6rge68m", 2424264227},
	{"thor1kchgh8t790zlfatdun975mu04xvumq3qjms65a", "thor1y0tksvjjwhvc7ltzce8k80t7sj8pt2zhpx2ku2", 39726507847},
	{"thor1kxw88w9pf4dx3euymzccqtys2kldqls77x2pch", "thor1lhufh0mwasa0lk9udppdegmvnkgqt08f0m9p5g", 61345859261},
	{"thor1kxw88w9pf4dx3euymzccqtys2kldqls77x2pch", "thor1vueafrh86p5c45yj5487wh5vvl2qnvpe5ajnrr", 6128960262},
	{"thor1m3fruxmw0x6a8hed6a6jns07h669uhcuc2jdg3", "thor1gex0nzts34rhlrl7dvy6u5gmsnd5z462k4w470", 336683},
	{"thor1m3fruxmw0x6a8hed6a6jns07h669uhcuc2jdg3", "thor1lhufh0mwasa0lk9udppdegmvnkgqt08f0m9p5g", 54523188932},
	{"thor1m99a3vr6ckafl3wm842zx4k7nq5cempxnx44p8", "thor1q0adkgxdph6660kflw5mnc0p7llhf3mf3f5jlv", 71590842979},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor16ltxavn8v2e8uwkc368wf0f4pv68kzv8ltf9rd", 647999509},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor18zg6y8ylus8n3tpzu5xxge3yyquj03vylstrl3", 42476265923},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor1atfrhw6ldw9rsa4dp0agfdkfmgdc4n53hz7lr9", 815195608},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor1drm88nvzn8qclrddac876ectt548cscgukm62d", 4503},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor1mge39hjzfr7uhxkr6ve829jsvytnzafwse5y65", 8427302448},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor1nmnq0r99fwfkp3pg8sdj4wlj2l96hx73m6835y", 7762082360},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor1qvmeavyusxyet7szr2azjzut7tamw4ycfg08ss", 7564594799},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor1stgurhwxy7h2z85nwe6cvwz4gtwzzjfdxk460x", 1557289231},
	{"thor1nlxtkz6wjrsz3wcez0vz577kl6xx7m5mdmysvy", "thor1xwqj94q8nwuajwkurv3h6cvwuqe6exy7448yd0", 2210457758},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor108sprrj7qw0w9vzwmwljrjt2rq5l8kcrxary02", 217350890},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1326qkkk8p0gs6qnut7pa5d28xzhq24tl6cerkj", 52625},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor133arshuuajtgjhpsylv3kpqlspn4cmgtag4khy", 690482815},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor133g0mey8dh7wpc4ax7pc6hp4dappgq405t845y", 108507701},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor13nhlg7cqspec5e0yv0n6nts3zcmfnc6hj8pjkf", 679},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor13unwxd9546m9vkclmyez924ceaasucjcwnme9r", 307613092},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor14wuvjehqkqm34hjtvfm5asgzu94f8yy592rl9z", 103572422},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor168z67fxgxlg6sf9tw6d4trnsvs3agcevr9kqls", 2030019477},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor177cr3puy4kg8c86lpe4ru5svyer6vuedf4s6ls", 255842231},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor17xn4e53auf4xla7g84ln2f4vjv2ks30fz6s7sa", 340921720},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor18vcd5p6jmcaswyl4hhw9rxcnluy0dknmvymsp5", 716086425},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor18wat4tvpwkfap34xplmq5vgvlqjynks24as4me", 104371045},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1alhxep8435qr5yl5eknzhkjj5e3y5hn70s7ews", 1137451251},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1c3mxxtjksx8l7c3sjuzjelsdw34qsfj5vnzcp9", 750010017},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1cjfvgdfephz4nk92w6yt8e6meeuvape6ysrj6q", 206877010},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1dk9y6ys5eqrcnut9z3ygjsa4al6flvcgxl8x2l", 404737},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 10357243412},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1fhuute7lqd9c8d5lezh6ghmnjx6q2wgnf68xe2", 3},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1g2gtespd6gm64fd73n929gudrmjf8e38vjw33e", 112191257},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1l9y2x4kmz24e6utz49xkzxgcfruwddwsyffzgc", 353746061},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1lcxptfm2286nvvpk8hm3tznsn5ess4zynj0je8", 14584586824},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1n5mdk4nj7sv8rjlmv3v66zch8zalettszaanll", 769050812},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1p8y207hu3r6rz56u4urecdx6mcg6q6dvnthamy", 1254560815},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1qht0hc2g4dgdlmf4lzut9uvvvtm99y7h906vju", 387},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1rjcuffd5e2qdqdsy0x2ewdrr3pcsx0wmxym9at", 1939827985},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1s2czferyfpc3m72u0crx9ngt97q590h8qu9zfl", 530614},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1sqeyv6mydu28ahr82p2en39tex0e4q2cf0vur9", 29635},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1tgzpq9hjv3rd2uqazec64lcwqa0v0d70w2l90m", 302783034},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1unrtm2agxlkrv6jfzse469petlqty99y9tfdmg", 137393783},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1uspymsu39f7kyu469whae2jv96x6mspau2u3fu", 1493979381},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1w5dmmrgk7zfq684g24dyvyp50ahqy5qs5wdjz8", 154251046},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1wdppnycv8jd3pqrlm62l95f6stxv5zx0e6lhpw", 215635861},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1whwjx86ygg7c929e6rfv3vs64rdh8ak63qux97", 295},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1y8pu5a3vt2aan06zqrnp0wqhn4ajnez52k3xrd", 46213},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1yes8zxhq4p3r5rp9vt6rk9fdxqf2mnm43apumu", 23150015328},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1ys9fu9nnh7wqcpeg6gfex8y2hauwfs3hlvux62", 228190115},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1ytr788y533g6svjf2p3yqhyughxhcdhmly7puk", 72387040},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1yvhsqjg8glpyeh2gfmqcd3hjl59gp887g5q6vy", 219645761},
	{"thor1s6xz88ell5c6erd7hspfjw8syce89mgn32z6gr", "thor1yx7lkw4438v7ym2lpyd3ur7nfl7vsd795qzu76", 52368229},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor126kvalffg0nsqzwelwtq5teq07t5dsegut0yur", 4088999849},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor129axzc4kjf70qjrmzkz50hrp2ktcr62v5au6sv", 6154469228},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor16cg20jxct0jrhdvurndqqlc7w2jedhwkuepgdt", 201682664},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor18p4tkugmac6e06syqhhm8q6hscxuvzw4x8m7qa", 3344408260},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor19apvrcnec7hytae75zgzltdnpy3q7jhcxvmd9u", 3357255288},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1aauxh6hvzrczp37uj8f7kvhlm8w9cq5xnggws4", 2789301963},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1aq756vlcyxarp80p57687pwgrquuslp029ddhh", 1427237812},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1cqm4j8zn9vqh2djulsvpx7lfw693qsd4n9vw40", 3571957552},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1e35dvgqnjmyya9qkk8fuaxza3am9rythd32d6t", 781552413},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 316443827},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1fwhnzngc7h02sn2wz94fpaa7ukpggarzny9ske", 1107374625},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1gj0unjy43d9qq3r4hngpjdagft53z3gh7pz3hs", 3228048033},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1hazs2ax64zdmy2cyzg82l3m56ep3nl8j2uzgjy", 2357338804},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1jnnep660vqjkkyjxguw84sstvawew25cs6dw5x", 7370886025},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1n9gcud325e25ywe9sycwwh6pqgalq03e5mj7dg", 10267738550},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1rdt05nnxwksm68q0jh206x0vvtrm90ty39sm4j", 1830919975},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1uzkqcgr952f29q09cpe5snc9d78duplxfe9075", 1195380973},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1w6r49emnu4e3tstqc2ng8v9zpxmwx5g3qh2t93", 873265647},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1wmlsapqmuh00fuzpghc0kh4flcjk8tu450078w", 478257244},
	{"thor1sl3k3u4yd7jwzhkgu4a5lj70vkfn6tlr7q5u33", "thor1wqthlnyqltf240xq0zhtwcnc99k9rwsvh65dsv", 6375365189},
	{"thor1sw793wdz5s7xnfmc6epkug4p2xhhhhaxxamkfd", "thor16y6hqd228pzw2g34rh4y6gzpudxn3rdyu2h0r8", 20684326410},
	{"thor1sw793wdz5s7xnfmc6epkug4p2xhhhhaxxamkfd", "thor1e0hj0p6trsrw8v5dnfjgmsgeu77mnk34qkgfur", 2870697313},
	{"thor1sw793wdz5s7xnfmc6epkug4p2xhhhhaxxamkfd", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 314721388},
	{"thor1sw793wdz5s7xnfmc6epkug4p2xhhhhaxxamkfd", "thor1fpyaj39rdlc5f80kulq55tqlvku4t66gq5pvqk", 43210611468},
	{"thor1sw793wdz5s7xnfmc6epkug4p2xhhhhaxxamkfd", "thor1n9gcud325e25ywe9sycwwh6pqgalq03e5mj7dg", 958838},
	{"thor1szdm49h0pq74jjc8adgk3evnkmsrwd5rtx0k68", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 11795023921},
	{"thor1szdm49h0pq74jjc8adgk3evnkmsrwd5rtx0k68", "thor1fwya3q0lvr4fufqhn5x94p4cuyvsejwqg9wk94", 20548043098},
	{"thor1szdm49h0pq74jjc8adgk3evnkmsrwd5rtx0k68", "thor1rth0kanms3cy82swu7pp6pak65e3qmrscxslml", 821648},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor12rtakf762ez8wad2ljz80hx3475ajqmdm6vxyu", 5141736104},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor170kk2ww8cra7la9lf4czwhhp3lld5xpp94ujsn", 5806863377},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor18uquyh2nl9chgxe63erhf68e0v9mwwwwqv9g3l", 251},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor18zg6y8ylus8n3tpzu5xxge3yyquj03vylstrl3", 9839274330},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor1dpc3jzxg0jwp5q4rzk0xp334stuas2t47vncc0", 35875466},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 10348693178},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor1hxrydan0eypp5eutah2dt63swnqwzuvc0elgmd", 21423900432},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor1tfm4q8u57qzsznpvh02s8j483aga63cl02k6jt", 1051366},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor1v02x3dww72jwqz8seux9jka8z08e8nq84y3vtg", 2189951102},
	{"thor1v882x92avxcmkaucm2h6cp6j2qc4lhll0vfsex", "thor1vwuyfwk85a247ett40j7uwk3k0s8cm5qaxevwl", 12739692921},
	{"thor1va3av7xeq57jyy5jr3wumw3ezg6vs6ldy9geqf", "thor1ju6gqug04986pr8m8a7jgyurg95z995pcdvwkw", 69529826431},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1326qkkk8p0gs6qnut7pa5d28xzhq24tl6cerkj", 10114815538},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor19kjgva3jky9g3r7l7v3eq65mr3cphpnw9w4jlv", 722929399},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1c2ej2t59upl2mwky9hj2y20wdst5gklyzpc4m4", 18978210207},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1djxtte785eqpqpsyhq49w67wklpack50gu683r", 238},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1dk9y6ys5eqrcnut9z3ygjsa4al6flvcgxl8x2l", 5774649},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1dzyv4x7y496s2r2q6zz58qkxe7knymvgv64xze", 855286697},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1enhmz57mpn4umspa8hqgqwwqpe02q4hpqmqr0k2zftxr8fu3nxmqjy3tqx", 5285399236},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1f47wux64gfn7m68jusqxke3k2scmqrtatyzl24", 15781884915},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1hftu6pxar7z52257vztmt669nmkctltfwxhtyh", 340921631},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1kp9xccz6nq65rnh73uy7wawlnrzu0fcsnc4c5a", 4407152867},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1kx3rnruyz7aj832jtntfu0ta2wgm5chs60wzmh", 1191452063},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1mdueq38s00tlflzqml757uxewwkpk8khry0t3v", 1196432756},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1ulq5hk4292pe0fc2h3azns8rla5rr67rpn5qrr", 6643871205},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1unrtm2agxlkrv6jfzse469petlqty99y9tfdmg", 352359891},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1upuw8yvg96uf4nd6jfuq9s25284dzr45kyzarc", 928047},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1vtrlsuydxrst9ufgapgxfvldy2lp07pjayptwm", 1469838347},
	{"thor1xskml0m3c6z2p3r5cmfwzn72zdgxtc898lfzzy", "thor1ys9fu9nnh7wqcpeg6gfex8y2hauwfs3hlvux62", 393166887},
}

// applySolSlashPoolLeg19to20 reverses the SOL pool legs of the phantom slash per the chosen
// mode: poolLegNone is a no-op (Reserve funds the full bond restore and the pool keeps the
// phantom RUNE), poolLegReclaimRune reclaims the phantom RUNE credit from the SOL pool back
// to the Reserve (reversing the slash's Bond->Asgard + pool.BalanceRune credit), and
// poolLegFullReversal (SOL pool-asset restore) is not yet implemented.
func applySolSlashPoolLeg19to20(ctx cosmos.Context, mgr *Mgrs, leg solSlashPoolLegMode) error {
	switch leg {
	case poolLegNone:
		return nil
	case poolLegFullReversal:
		// B: complete reversal, BTC/ZEC/TRON parity. Restore the 30.64 SOL asset depth the
		// slash marked down AND debit the 6,140.85 phantom RUNE it credited, moving that RUNE
		// Asgard->Reserve to fund the bond restore.
		k := mgr.Keeper()
		// HaltSigningSOL is the only guard against a signed-but-unobserved SOL outbound later
		// spending the unaccounted vault SOL that backs the restored pool depth (same gate the
		// ZEC/TRON pool-asset refunds use). Must be set before the upgrade (launch plan).
		if !signingHalted19to20(ctx, mgr, common.SOLChain) {
			return fmt.Errorf("sol slash full reversal skipped: HaltSigningSOL not set; an unobserved retry could spend the backing SOL")
		}
		sol, err := common.NewAsset(solSlashAsset)
		if err != nil {
			return fmt.Errorf("parse %s: %w", solSlashAsset, err)
		}
		pool, err := k.GetPool(ctx, sol)
		if err != nil {
			return fmt.Errorf("get SOL pool: %w", err)
		}
		if pool.IsEmpty() {
			return fmt.Errorf("SOL pool is empty")
		}
		// Solvency gate: the restored 30.64 SOL depth must be backed by unaccounted SOL the
		// vault still holds (the phantom slash marked the pool down but the SOL never left).
		assetCredit := cosmos.NewUint(solSlashPoolAssetMarkdown)
		freeSOL, err := unaccountedVaultAsset19to20(ctx, mgr, sol)
		if err != nil {
			return fmt.Errorf("sol solvency: %w", err)
		}
		if freeSOL.LT(assetCredit) {
			return fmt.Errorf("unaccounted SOL %s below markdown %d; vault does not hold the reversed SOL", freeSOL, solSlashPoolAssetMarkdown)
		}
		credit := cosmos.NewUint(solSlashPoolRuneCredit)
		if pool.BalanceRune.LT(credit) {
			return fmt.Errorf("SOL pool BalanceRune %s below phantom credit %d; pool drifted", pool.BalanceRune, solSlashPoolRuneCredit)
		}
		pool.BalanceRune = pool.BalanceRune.Sub(credit)
		pool.BalanceAsset = pool.BalanceAsset.Add(assetCredit)
		if err := k.SetPool(ctx, pool); err != nil {
			return fmt.Errorf("set SOL pool: %w", err)
		}
		if err := k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(common.NewCoin(common.RuneAsset(), credit))); err != nil {
			return fmt.Errorf("sol-slash asgard->reserve reclaim: %w", err)
		}
		if err := mgr.EventMgr().EmitEvent(ctx, NewEventSlash(sol, []PoolAmt{
			{Asset: sol, Amount: int64(solSlashPoolAssetMarkdown)},
			{Asset: common.RuneAsset(), Amount: -int64(solSlashPoolRuneCredit)},
		})); err != nil {
			ctx.Logger().Error("emit sol slash full-reversal event", "error", err)
		}
		return nil
	case poolLegReclaimRune:
		k := mgr.Keeper()
		sol, err := common.NewAsset(solSlashAsset)
		if err != nil {
			return fmt.Errorf("parse %s: %w", solSlashAsset, err)
		}
		pool, err := k.GetPool(ctx, sol)
		if err != nil {
			return fmt.Errorf("get SOL pool: %w", err)
		}
		if pool.IsEmpty() {
			return fmt.Errorf("SOL pool is empty")
		}
		credit := cosmos.NewUint(solSlashPoolRuneCredit)
		if pool.BalanceRune.LT(credit) {
			return fmt.Errorf("SOL pool BalanceRune %s below phantom credit %d; pool drifted", pool.BalanceRune, solSlashPoolRuneCredit)
		}
		pool.BalanceRune = pool.BalanceRune.Sub(credit)
		if err := k.SetPool(ctx, pool); err != nil {
			return fmt.Errorf("set SOL pool: %w", err)
		}
		if err := k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(common.NewCoin(common.RuneAsset(), credit))); err != nil {
			return fmt.Errorf("sol-slash asgard->reserve reclaim: %w", err)
		}
		if err := mgr.EventMgr().EmitEvent(ctx, NewEventSlash(sol, []PoolAmt{
			{Asset: common.RuneAsset(), Amount: -int64(solSlashPoolRuneCredit)},
		})); err != nil {
			ctx.Logger().Error("emit sol slash pool-reclaim event", "error", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown sol-slash pool leg %d", int(leg))
	}
}

func runSolSlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) {
	cacheCtx, commit := ctx.CacheContext()
	if err := applySolSlashRefund19to20(cacheCtx, mgr); err != nil {
		ctx.Logger().Error("sol slash refund skipped: state does not match the pinned figures", "error", err)
		return
	}
	commit()
}

func applySolSlashRefund19to20(ctx cosmos.Context, mgr *Mgrs) error {
	k := mgr.Keeper()

	// Validate every row + the pinned sum up front: a bad address or a sum mismatch is a spec
	// error and aborts the whole step before any write.
	total := cosmos.ZeroUint()
	for _, r := range mainnetSolSlashRefunds19to20 {
		if _, err := cosmos.AccAddressFromBech32(r.node); err != nil {
			return fmt.Errorf("bad sol-slash node %s: %w", r.node, err)
		}
		if _, err := cosmos.AccAddressFromBech32(r.provider); err != nil {
			return fmt.Errorf("bad sol-slash provider %s: %w", r.provider, err)
		}
		total = total.Add(cosmos.NewUint(r.amount))
	}
	if !total.Equal(cosmos.NewUint(solSlashBondRefundTotal)) {
		return fmt.Errorf("sol-slash refund sum %s != pinned %d", total, solSlashBondRefundTotal)
	}

	// Pool leg (done before the bond restore so a pool drift aborts the whole step rather than
	// leaving a half-applied reversal).
	if err := applySolSlashPoolLeg19to20(ctx, mgr, solSlashPoolLeg); err != nil {
		return err
	}

	// The Reserve must cover the full bond restore (its slash half plus, under ReclaimRune,
	// the reclaimed pool half).
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(cosmos.NewUint(solSlashBondRefundTotal)) {
		return fmt.Errorf("reserve %s below sol-slash refund total %d", bal, solSlashBondRefundTotal)
	}

	refunds := mainnetSolSlashRefunds19to20
	for i := 0; i < len(refunds); {
		// refunds are grouped by node (sorted); consume the contiguous run.
		nodeStr := refunds[i].node
		j := i
		for j < len(refunds) && refunds[j].node == nodeStr {
			j++
		}
		group := refunds[i:j]
		i = j

		nodeAddr, err := cosmos.AccAddressFromBech32(nodeStr)
		if err != nil {
			ctx.Logger().Error("sol slash refund: bad node address", "node", nodeStr, "error", err)
			continue
		}
		na, err := k.GetNodeAccount(ctx, nodeAddr)
		if err != nil || na.IsEmpty() {
			ctx.Logger().Error("sol slash refund: node account missing", "node", nodeStr, "error", err)
			continue
		}
		bp, err := k.GetBondProviders(ctx, nodeAddr)
		if err != nil {
			ctx.Logger().Error("sol slash refund: fail to get bond providers", "node", nodeStr, "error", err)
			continue
		}
		// Reconcile the provider ledger to the current node bond before crediting (no-op when
		// the providers already sum to node.Bond), matching the canonical bond-in flow.
		bp.Adjust(na.Bond)

		nodeTotal := cosmos.ZeroUint()
		for _, r := range group {
			provAddr, perr := cosmos.AccAddressFromBech32(r.provider)
			if perr != nil {
				ctx.Logger().Error("sol slash refund: bad provider address", "node", nodeStr, "provider", r.provider, "error", perr)
				nodeTotal = cosmos.ZeroUint()
				break
			}
			// A provider that fully unbonded since the slash is re-added so it is restored
			// exactly as deducted and the node.Bond<->providers sum invariant holds.
			if !bp.Has(provAddr) {
				bp.Providers = append(bp.Providers, NewBondProvider(provAddr))
			}
			bp.Bond(cosmos.NewUint(r.amount), provAddr)
			nodeTotal = nodeTotal.Add(cosmos.NewUint(r.amount))
		}
		if nodeTotal.IsZero() {
			continue // nothing persisted yet (na/bp are local copies)
		}
		na.Bond = na.Bond.Add(nodeTotal)

		// Fund the Bond module from the Reserve BEFORE persisting the ledger, so a funding
		// failure skips the node cleanly rather than leaving bonded amounts without backing.
		refundCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), nodeTotal))
		if err := k.SendFromModuleToModule(ctx, ReserveName, BondName, refundCoins); err != nil {
			ctx.Logger().Error("sol slash refund: fail to fund Bond from Reserve; skipping node", "node", nodeStr, "amount", nodeTotal, "error", err)
			continue
		}
		if err := k.SetNodeAccount(ctx, na); err != nil {
			ctx.Logger().Error("sol slash refund: fail to set node account", "node", nodeStr, "error", err)
		}
		if err := k.SetBondProviders(ctx, bp); err != nil {
			ctx.Logger().Error("sol slash refund: fail to set bond providers", "node", nodeStr, "error", err)
		}
		// Emit the inverse of the slash's BondCost: BondPaid (a bond INCREASE, as in the
		// canonical bond-in at handler_bond.go). BondReturned would be wrong here — the
		// codebase uses it for bond LEAVING the node (unbond/payout) and Midgard counts it as
		// a decrease, so it would double the apparent reduction instead of restoring the bond.
		if err := mgr.EventMgr().EmitEvent(ctx, NewEventBond(nodeTotal, BondPaid, common.Tx{}, &na, nil)); err != nil {
			ctx.Logger().Error("sol slash refund: emit bond paid event", "node", nodeStr, "error", err)
		}
	}

	ctx.Logger().Info("refunded phantom sol slash at h27591961",
		"refund_total", solSlashBondRefundTotal, "providers", len(mainnetSolSlashRefunds19to20), "pool_leg", int(solSlashPoolLeg))
	return nil
}
