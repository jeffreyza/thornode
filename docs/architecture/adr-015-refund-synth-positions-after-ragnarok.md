# ADR 015: Refund Synth Positions After Ragnarok

## Changelog

- 2024-05-08: Created
- 2024-05-19: Incorporated comments. Added details.

## Status

Proposed

## Context

During a Ragnarok, all funds remaining in pools are returned in a best-effort fashion and any synthetic tokens are burned. When these tokens are burned, they are effectively converted to RUNE and transferred to the Reserve. While synthetic tokens are tracked and associated with specific addresses, enumerating these addresses is not feasible in the current code and therefore this converted RUNE cannot be returned to its owner during the Ragnarok.

While it can't be done in the current code, Multipartite has come up with a method to identify these addresses using THORNode logs and the Flipside databases.

After the recent Ragnarok of the BNB blockchain, roughly 24,601 RUNE was transferred to the Reserve in this manner. This proposal seeks to return the RUNE liquidity behind any burned synthetic tokens to their respective owners.

### Method

By searching through THORNode logs it is possible to identify all pools that sent synth-associated RUNE to the reserve.

```text
$ docker logs fullnode-v1 |& grep -A3 'redeeming synth to reserve'

3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.ETH-1C9
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=115286909700 synth_supply=283716656
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1292 > setting pool to staged pool=BNB.ETH-1C9

3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.TWT-8C2
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=315117680195 synth_supply=2018826348469
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1292 > setting pool to staged pool=BNB.TWT-8C2

5:47PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.BTCB-1DE
5:47PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=1543832682001 synth_supply=197741221
5:47PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1292 > setting pool to staged pool=BNB.BTCB-1DE

7:00PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.BUSD-BD1
7:00PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=278944032906 synth_supply=2178219990168
7:00PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1292 > setting pool to staged pool=BNB.BUSD-BD1

6:50PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.BNB
6:50PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=206909292040 synth_supply=2178708870
6:50PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1292 > setting pool to staged pool=BNB.BNB
```

From these logs, we can identify the pools that transferred RUNE to the Reserve as well as the synth value in RUNE at the time of transfer.

In the THORNode code, pools are set to `Staged` immediately after RUNE transfer to Reserve. We can use this to identify the block number where the RUNE was transferred to Resreve for each pool.

```text
$ docker run --rm --network host -e RPC_ENDPOINT=http://localhost:27148 cosmoscan 'block_events_listener(lambda h,e: (h,e), types={"pool"}), start=15206145, end=15269760, progress=True'

[15207120, {"type": "pool", "pool": "BNB.ETH-1C9", "pool_status": "Staged"}]
[15207120, {"type": "pool", "pool": "BNB.TWT-8C2", "pool_status": "Staged"}]
[15208560, {"type": "pool", "pool": "BNB.BTCB-1DE", "pool_status": "Staged"}]
[15209280, {"type": "pool", "pool": "BNB.BUSD-BD1", "pool_status": "Staged"}]
[15265440, {"type": "pool", "pool": "BNB.BNB", "pool_status": "Staged"}]
```

We are interested in all addresses that still held synthetic positions at the block number just before the RUNE was transferred. We can use the Flipside databases to identify these addresses.

With a given address, it is then possible to check exactly how much of a synthetic position that address held at the RUNE transfer height where the synths' value was effectively converted to RUNE.

For example, the following is the query in the case of the BNB.BNB pool:

```sql
WITH
transfers_to AS (
  SELECT to_address AS address, asset, amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BNB'
    AND block_id <= 15265440
),

transfers_from AS (
  SELECT from_address AS address, asset, -1 * amount_e8 AS amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BNB'
    AND block_id <= 15265440
)

SELECT address, asset,
  SUM(amount_e8) / 1e8 AS amount,
  amount * 206909292040 / 2178708870 AS rune_amount
FROM (
  (SELECT * FROM transfers_to)
  UNION ALL
  (SELECT * FROM transfers_from)
)
GROUP BY address, asset
ORDER BY asset ASC, amount DESC
```

### Summary

Using this method, we find roughly 24,601 RUNE to be returned to 1,309 addresses. As a refund must be done by hand with a store-migration, we suggest that only balances greater than 10 RUNE be returned. This reduces the number of addresses which must be refunded to 46 and the amount of RUNE to 24,192.83.

```text
ADDRESS,ASSET,AMOUNT,RUNE_AMOUNT
thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0,BNB/BNB,15.884561,1508.537150664005
thor1vsnj373g9uqke6z5fdpps8p8udrls3a8m75t86,BNB/BNB,1.233685,117.16154162
thor1hkyzhmf85kfl87mgjdh42yplgumj7gsrlvjyk9,BNB/BNB,0.495036,47.012957859906
thor1tcr7q0tmj3863d03w6yfc2w5ghzfqup65j0c29,BNB/BNB,0.467588,44.406255181033
thor1rgdhwaj3nr937ftlnrh7sdw38d9ene6yl494de,BNB/BNB,0.387236,36.775324925538
thor13uw2awf9k2nqnmzqz8ugckfljtwzngqfy6kchf,BNB/BNB,0.266564,25.315254040046
thor1er9ys0ymy9lt4tet77ztaexh6dp3xdw5m7rkpj,BNB/BNB,0.25118,23.854254549672
thor1fq3vlugshpg90lewgcf44nux2qrxk85597rsp4,BNB/BNB,0.239822,22.775599309703
thor19gu67axrmeeknl2k2tf38h92vek3suwnjkn5j8,BNB/BNB,0.19907,18.905432172956
thor1afs68s095v58tc6e560w2xfmzfy4tdzny5329e,BNB/BNB,0.195046,18.523277860081
thor1zl6el90vw3ncjzh28mcautrkjn9jagrec47ac2,BNB/BNB,0.121589,11.54715724357
thor1fq9sculy0ej2p9sa52e304m0hq96edgq3qg95m,BNB/BNB,0.115925,11.009254155071
thor12z9pwgw50pepl7mazv4nf2868c6ke882a6aw8x,BNB/BTCB-1DE,1.035567,8085.022287799718
thor1mq8mzl9272dzhee3flkndyz2lux9nvzq8zn0f7,BNB/BTCB-1DE,0.295774,2309.207788729917
thor1uunxme33cufhtcm00ygkfx6th3vvjmatv8tanl,BNB/BTCB-1DE,0.150318,1173.583534679531
thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0,BNB/BTCB-1DE,0.1197,934.538439183197
thor1eyvhjlezafz36cz3vev60u6p2zn24dhu9gv0wf,BNB/BTCB-1DE,0.088792,693.229215471633
thor1tp8l7ygmnhmny9dknjjj8d65x49t28nuyc4e32,BNB/BTCB-1DE,0.083916,655.160632101063
thor1s9cv0mcp80lehzyjhjtkcaq2yjc4ez36p8qh33,BNB/BTCB-1DE,0.064561,504.049592081089
thor17kq48hspxnaku6pd4crj9hnywjcpehvxsemfsr,BNB/BTCB-1DE,0.049957,390.031218097535
thor125rz8fe8spjhjsknfmejddtxfh44v9jn0klhcx,BNB/BTCB-1DE,0.029797,232.635270445629
thor10rthwfd27zhs3lrk2ck8gv2yx3zgz3wtuvtle8,BNB/BTCB-1DE,0.018879,147.394746811525
thor10j3d4dgq0sekw3vywtt8kgeeympujsmcnmz0ha,BNB/BTCB-1DE,0.016424,128.227730368796
thor1kv4jdrnekdfqcajez78d2drvhp3ep6akx6865j,BNB/BTCB-1DE,0.007971,62.232296564154
thor1l7p2rlckkclsgauall0v4hfy94rdpk2j8dn9qn,BNB/BTCB-1DE,0.004221,32.954776539618
thor1mge35algk7x6r4gygauvxcwn5nhqcchmaayuyr,BNB/BTCB-1DE,0.002744,21.423337319287
thor1nc8kha8hn74h0w7zdyclncq5h039wecgeftnyn,BNB/BTCB-1DE,0.001456,11.367485108193
thor1x4d5g75v67affmr9qjuu75swjsgme4jscxj7pl,BNB/BUSD-BD1,9170.798605,1174.417441486301
thor1dmgdpkqngg3amua5hffjcharngzdp4wv39azjs,BNB/BUSD-BD1,3496.943135,447.820433805043
thor1atev7k3xzsqsenrwjht3k3r70t9mtz0p58qn6d,BNB/BUSD-BD1,2776.701122,355.585851126799
thor1p8as4v08l6t04tahqgajgw5ycj2a5k3ygthzql,BNB/BUSD-BD1,2193.01509,280.838701411942
thor1fptyt9rkukvn80sda7q8e446ft2lp6jkngh8pm,BNB/BUSD-BD1,1823.603454,233.531647021951
thor1vqd6djdqn4hlrquxuwz2na3mac8qm8qgwfu7ps,BNB/BUSD-BD1,1385.756848,177.460773270386
thor155cljp4fcarppqupzh9s3uvu6525hd5gtktw03,BNB/BUSD-BD1,350.860094,44.931333863385
thor1wudr3yyc0d436k8cml7c8dvtqxwsvwlztvgq8s,BNB/BUSD-BD1,103.782854,13.290488551169
thor1j47es49m8llprlpcxrt3a2hqyhperwc7ta6ksm,BNB/ETH-1C9,1.132255,460.086417846309
thor1rpu6ndvg0r25y6xqdl2svqlwglc3yhh3wv7j9v,BNB/ETH-1C9,1.009999,410.408275466528
thor1kv4jdrnekdfqcajez78d2drvhp3ep6akx6865j,BNB/ETH-1C9,0.239639,97.376164456127
thor140dy78lz5vv84gknn2wdwv7ed4nhs2kauwj9w2,BNB/ETH-1C9,0.089441,36.343923673194
thor1tu9xulcjw76mp7ky7v9l2hrwk2chvglapyycra,BNB/ETH-1C9,0.031706,12.883581847053
thor196svmy67vm4ya3rnlfnjq4sc9u5c5skurcanf9,BNB/ETH-1C9,0.031393,12.756395790214
thor1dsk8smfqt6xxjs8lzuy4hpxrh0wfklf5twjhue,BNB/ETH-1C9,0.02748,11.166366907116
thor19pkfd9ygch6dfa067ddn0fwul8g3x0sy8nxnvr,BNB/ETH-1C9,0.027222,11.061529837901
thor1a8m2shzvyya0ckvq76fxsnd5800hm0uxxjuy3g,BNB/ETH-1C9,0.025754,10.46501504097
thor1rzxvqhepnqqcn7973jp4y0ygasr709gpj673lw,BNB/TWT-8C2,20000.788343,3121.913892939335
thor1sth9gz5asawsvfrq08ag7wzwqqlrjxl0egxuym,BNB/TWT-8C2,87.193871,13.610051393266

Total:
  addresses: 46
  RUNE: 24192.830096617447
```

Note that two addresses held multiple positions. Specifically,

```text
thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0:
thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0,BNB/BNB,15.884561,1508.537150664005
thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0,BNB/BTCB-1DE,0.1197,934.538439183197

thor1kv4jdrnekdfqcajez78d2drvhp3ep6akx6865j:
thor1kv4jdrnekdfqcajez78d2drvhp3ep6akx6865j,BNB/BTCB-1DE,0.007971,62.232296564154
thor1kv4jdrnekdfqcajez78d2drvhp3ep6akx6865j,BNB/ETH-1C9,0.239639,97.376164456127
```

These positions should most likely be combined to simplify the store-migration.

### Details

The following are the details for each pool.

#### BNB.BNB

```text
[15265440, {"type": "pool", "pool": "BNB.BNB", "pool_status": "Staged"}]
6:50PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.BNB
6:50PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=206909292040 synth_supply=2178708870
```

```text
ADDRESS,ASSET,AMOUNT,RUNE_AMOUNT
thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0,BNB/BNB,15.884561,1508.537150664005
thor1vsnj373g9uqke6z5fdpps8p8udrls3a8m75t86,BNB/BNB,1.233685,117.16154162
thor1hkyzhmf85kfl87mgjdh42yplgumj7gsrlvjyk9,BNB/BNB,0.495036,47.012957859906
thor1tcr7q0tmj3863d03w6yfc2w5ghzfqup65j0c29,BNB/BNB,0.467588,44.406255181033
thor1rgdhwaj3nr937ftlnrh7sdw38d9ene6yl494de,BNB/BNB,0.387236,36.775324925538
thor13uw2awf9k2nqnmzqz8ugckfljtwzngqfy6kchf,BNB/BNB,0.266564,25.315254040046
thor1er9ys0ymy9lt4tet77ztaexh6dp3xdw5m7rkpj,BNB/BNB,0.25118,23.854254549672
thor1fq3vlugshpg90lewgcf44nux2qrxk85597rsp4,BNB/BNB,0.239822,22.775599309703
thor19gu67axrmeeknl2k2tf38h92vek3suwnjkn5j8,BNB/BNB,0.19907,18.905432172956
thor1afs68s095v58tc6e560w2xfmzfy4tdzny5329e,BNB/BNB,0.195046,18.523277860081
thor1zl6el90vw3ncjzh28mcautrkjn9jagrec47ac2,BNB/BNB,0.121589,11.54715724357
thor1fq9sculy0ej2p9sa52e304m0hq96edgq3qg95m,BNB/BNB,0.115925,11.009254155071

total rune: 1885.823460
total addresses: 12
```

The following is the SQL code used to query the Flipside database:

```sql
WITH
transfers_to AS (
  SELECT to_address AS address, asset, amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BNB'
    AND block_id <= 15265440
),

transfers_from AS (
  SELECT from_address AS address, asset, -1 * amount_e8 AS amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BNB'
    AND block_id <= 15265440
)

SELECT address, asset,
  SUM(amount_e8) / 1e8 AS amount,
  amount * 206909292040 / 2178708870 AS rune_amount
FROM (
  (SELECT * FROM transfers_to)
  UNION ALL
  (SELECT * FROM transfers_from)
)
GROUP BY address, asset
ORDER BY asset ASC, amount DESC
```

#### BNB.BTCB

```text
[15208560, {"type": "pool", "pool": "BNB.BTCB-1DE", "pool_status": "Staged"}]
5:47PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.BTCB-1DE
5:47PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=1543832682001 synth_supply=197741221
```

```text
ADDRESS,ASSET,AMOUNT,RUNE_AMOUNT
thor12z9pwgw50pepl7mazv4nf2868c6ke882a6aw8x,BNB/BTCB-1DE,1.035567,8085.022287799718
thor1mq8mzl9272dzhee3flkndyz2lux9nvzq8zn0f7,BNB/BTCB-1DE,0.295774,2309.207788729917
thor1uunxme33cufhtcm00ygkfx6th3vvjmatv8tanl,BNB/BTCB-1DE,0.150318,1173.583534679531
thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0,BNB/BTCB-1DE,0.1197,934.538439183197
thor1eyvhjlezafz36cz3vev60u6p2zn24dhu9gv0wf,BNB/BTCB-1DE,0.088792,693.229215471633
thor1tp8l7ygmnhmny9dknjjj8d65x49t28nuyc4e32,BNB/BTCB-1DE,0.083916,655.160632101063
thor1s9cv0mcp80lehzyjhjtkcaq2yjc4ez36p8qh33,BNB/BTCB-1DE,0.064561,504.049592081089
thor17kq48hspxnaku6pd4crj9hnywjcpehvxsemfsr,BNB/BTCB-1DE,0.049957,390.031218097535
thor125rz8fe8spjhjsknfmejddtxfh44v9jn0klhcx,BNB/BTCB-1DE,0.029797,232.635270445629
thor10rthwfd27zhs3lrk2ck8gv2yx3zgz3wtuvtle8,BNB/BTCB-1DE,0.018879,147.394746811525
thor10j3d4dgq0sekw3vywtt8kgeeympujsmcnmz0ha,BNB/BTCB-1DE,0.016424,128.227730368796
thor1kv4jdrnekdfqcajez78d2drvhp3ep6akx6865j,BNB/BTCB-1DE,0.007971,62.232296564154
thor1l7p2rlckkclsgauall0v4hfy94rdpk2j8dn9qn,BNB/BTCB-1DE,0.004221,32.954776539618
thor1mge35algk7x6r4gygauvxcwn5nhqcchmaayuyr,BNB/BTCB-1DE,0.002744,21.423337319287
thor1nc8kha8hn74h0w7zdyclncq5h039wecgeftnyn,BNB/BTCB-1DE,0.001456,11.367485108193

total rune: 15381.058351
total addresses: 15
```

The following is the SQL code used to query the Flipside database:

```sql
WITH
transfers_to AS (
  SELECT to_address AS address, asset, amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BTCB-1DE'
    AND block_id <= 15208560
),

transfers_from AS (
  SELECT from_address AS address, asset, -1 * amount_e8 AS amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BTCB-1DE'
    AND block_id <= 15208560
)

SELECT address, asset,
  SUM(amount_e8) / 1e8 AS amount,
  amount * 1543832682001 / 197741221 AS rune_amount
FROM (
  (SELECT * FROM transfers_to)
  UNION ALL
  (SELECT * FROM transfers_from)
)
GROUP BY address, asset
ORDER BY asset ASC, amount DESC
```

#### BNB.BUSD

```text
[15209280, {"type": "pool", "pool": "BNB.BUSD-BD1", "pool_status": "Staged"}]
7:00PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.BUSD-BD1
7:00PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=278944032906 synth_supply=2178219990168
```

```text
ADDRESS,ASSET,AMOUNT,RUNE_AMOUNT
thor1x4d5g75v67affmr9qjuu75swjsgme4jscxj7pl,BNB/BUSD-BD1,9170.798605,1174.417441486301
thor1dmgdpkqngg3amua5hffjcharngzdp4wv39azjs,BNB/BUSD-BD1,3496.943135,447.820433805043
thor1atev7k3xzsqsenrwjht3k3r70t9mtz0p58qn6d,BNB/BUSD-BD1,2776.701122,355.585851126799
thor1p8as4v08l6t04tahqgajgw5ycj2a5k3ygthzql,BNB/BUSD-BD1,2193.01509,280.838701411942
thor1fptyt9rkukvn80sda7q8e446ft2lp6jkngh8pm,BNB/BUSD-BD1,1823.603454,233.531647021951
thor1vqd6djdqn4hlrquxuwz2na3mac8qm8qgwfu7ps,BNB/BUSD-BD1,1385.756848,177.460773270386
thor155cljp4fcarppqupzh9s3uvu6525hd5gtktw03,BNB/BUSD-BD1,350.860094,44.931333863385
thor1wudr3yyc0d436k8cml7c8dvtqxwsvwlztvgq8s,BNB/BUSD-BD1,103.782854,13.290488551169

total rune: 2727.876671
total addresses: 8
```

The following is the SQL code used to query the Flipside database:

```sql
WITH
transfers_to AS (
  SELECT to_address AS address, asset, amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BUSD-BD1'
    AND block_id <= 15209280
),

transfers_from AS (
  SELECT from_address AS address, asset, -1 * amount_e8 AS amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/BUSD-BD1'
    AND block_id <= 15209280
)

SELECT address, asset,
  SUM(amount_e8) / 1e8 AS amount,
  amount * 278944032906 / 2178219990168 AS rune_amount
FROM (
  (SELECT * FROM transfers_to)
  UNION ALL
  (SELECT * FROM transfers_from)
)
GROUP BY address, asset
ORDER BY asset ASC, amount DESC
```

#### BNB.ETH

```text
[15207120, {"type": "pool", "pool": "BNB.ETH-1C9", "pool_status": "Staged"}]
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.ETH-1C9
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=115286909700 synth_supply=283716656
```

```text
ADDRESS,ASSET,AMOUNT,RUNE_AMOUNT
thor1j47es49m8llprlpcxrt3a2hqyhperwc7ta6ksm,BNB/ETH-1C9,1.132255,460.086417846309
thor1rpu6ndvg0r25y6xqdl2svqlwglc3yhh3wv7j9v,BNB/ETH-1C9,1.009999,410.408275466528
thor1kv4jdrnekdfqcajez78d2drvhp3ep6akx6865j,BNB/ETH-1C9,0.239639,97.376164456127
thor140dy78lz5vv84gknn2wdwv7ed4nhs2kauwj9w2,BNB/ETH-1C9,0.089441,36.343923673194
thor1tu9xulcjw76mp7ky7v9l2hrwk2chvglapyycra,BNB/ETH-1C9,0.031706,12.883581847053
thor196svmy67vm4ya3rnlfnjq4sc9u5c5skurcanf9,BNB/ETH-1C9,0.031393,12.756395790214
thor1dsk8smfqt6xxjs8lzuy4hpxrh0wfklf5twjhue,BNB/ETH-1C9,0.02748,11.166366907116
thor19pkfd9ygch6dfa067ddn0fwul8g3x0sy8nxnvr,BNB/ETH-1C9,0.027222,11.061529837901
thor1a8m2shzvyya0ckvq76fxsnd5800hm0uxxjuy3g,BNB/ETH-1C9,0.025754,10.46501504097

total rune: 1062.547671
total addresses: 9
```

The following is the SQL code used to query the Flipside database:

```sql
WITH
transfers_to AS (
  SELECT to_address AS address, asset, amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/ETH-1C9'
    AND block_id <= 15207120
),

transfers_from AS (
  SELECT from_address AS address, asset, -1 * amount_e8 AS amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/ETH-1C9'
    AND block_id <= 15207120
)

SELECT address, asset,
  SUM(amount_e8) / 1e8 AS amount,
  amount * 115286909700 / 283716656 AS rune_amount
FROM (
  (SELECT * FROM transfers_to)
  UNION ALL
  (SELECT * FROM transfers_from)
)
GROUP BY address, asset
ORDER BY asset ASC, amount DESC
```

#### BNB.TWT

```text
[15207120, {"type": "pool", "pool": "BNB.TWT-8C2", "pool_status": "Staged"}]
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1287 > redeeming synth to reserve pool=BNB.TWT-8C2
3:17PM INF gitlab.com/thorchain/thornode/x/thorchain/manager_network_current.go:1750 > sending synth redeem RUNE to Reserve rune_amount=315117680195 synth_supply=2018826348469
```

```text
ADDRESS,ASSET,AMOUNT,RUNE_AMOUNT
thor1rzxvqhepnqqcn7973jp4y0ygasr709gpj673lw,BNB/TWT-8C2,20000.788343,3121.913892939335
thor1sth9gz5asawsvfrq08ag7wzwqqlrjxl0egxuym,BNB/TWT-8C2,87.193871,13.610051393266

total rune: 3135.523944
total addresses: 2
```

The following is the SQL code used to query the Flipside database:

```sql
WITH
transfers_to AS (
  SELECT to_address AS address, asset, amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/TWT-8C2'
    AND block_id <= 15207120
),

transfers_from AS (
  SELECT from_address AS address, asset, -1 * amount_e8 AS amount_e8
  FROM (thorchain.core.fact_transfer_events AS reftable INNER JOIN thorchain.core.dim_block
    ON reftable.dim_block_id = dim_block.dim_block_id)
  WHERE asset = 'BNB/TWT-8C2'
    AND block_id <= 15207120
)

SELECT address, asset,
  SUM(amount_e8) / 1e8 AS amount,
  amount * 315117680195 / 2018826348469 AS rune_amount
FROM (
  (SELECT * FROM transfers_to)
  UNION ALL
  (SELECT * FROM transfers_from)
)
GROUP BY address, asset
ORDER BY asset ASC, amount DESC
```

## Alternative Approaches

## Decision

## Detailed Design

The implementation of the store-migration mechanism introduces a small amount of risk which must be managed through rigorous testing and quality assurance measures. The return process must also be implemented with utmost transparency and accountability.

Moreover, while we believe the process used to identify these balances as well as the amounts to return are correct and explained in detail above, the results should be verified.

## Consequences

### Positive

### Negative

### Neutral

## References

- https://discord.com/channels/838986635756044328/1225291406344716378
