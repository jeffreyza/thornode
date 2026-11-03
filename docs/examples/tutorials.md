# Tutorials

## **User Transaction History**

Actions within THORChain can be obtained from Midgard which will list the actions taken by any given address.

**Request**: _List actions by the address bc1qn9esxuw8ca7ts8l6w66kdh800s09msvutydc46_

[https://gateway.liquify.com/chain/thorchain_midgard/v2/actions?address=bc1qn9esxuw8ca7ts8l6w66kdh800s09msvutydc46](https://gateway.liquify.com/chain/thorchain_midgard/v2/actions?address=bc1qn9esxuw8ca7ts8l6w66kdh800s09msvutydc46)

**Response:**

```json
{
  "actions": [
    {
      "date": "1619430821907265922",
      "height": "249758",
      "in": [
        {
          "address": "bc1qymjw5ugnah5jpa4eg4s5yqvapmu6pkxvhzcgf4",
          "coins": [
            {
              "amount": "20000000",
              "asset": "BTC.BTC"
            }
          ],
          "txID": "10BDC8C2BAB5CC3E2305782D9E2FA22630FC72852CBCBA05EDB8B841F670742F"
        }
      ],
      "metadata": {
        "refund": {
          "affiliateAddress": "",
          "affiliateFee": "0",
          "memo": "=:ETH.ETH:0xdeed0578e052bda159ee2f36ca590cad7fe1f761:420283628",
          "networkFees": [
            {
              "amount": "198000",
              "asset": "BTC.BTC"
            }
          ],
          "reason": "emit asset 406568399 less than price limit 420283628",
          "txType": "swap"
        }
      },
      "out": [
        {
          "address": "bc1qymjw5ugnah5jpa4eg4s5yqvapmu6pkxvhzcgf4",
          "coins": [
            {
              "amount": "19882091",
              "asset": "BTC.BTC"
            }
          ],
          "height": "249761",
          "txID": "7A69F3F16EBE321B70D1D7DB046E57D598AB579C94F591AA99CD09B0516C2ED6"
        }
      ],
      "pools": [],
      "status": "success",
      "type": "refund"
    },
    ....
  ],
  "count": "491"
}
```

The Action endpoint is very flexible, see the [docs](https://gateway.liquify.com/chain/thorchain_midgard/v2/doc#operation/GetActions).

## Check the status of a Transaction

Transactions can [take time to fully process](../concepts/delays.md) once sent to THORChain.

**Request**: Get the status for tx 27208951434B8D7ACB416C6B0B5DAE6449A3A3D2C4484F0B36803A180F24B1E3. [https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/status/27208951434B8D7ACB416C6B0B5DAE6449A3A3D2C4484F0B36803A180F24B1E3](https://gateway.liquify.com/chain/thorchain_api/thorchain/tx/status/27208951434B8D7ACB416C6B0B5DAE6449A3A3D2C4484F0B36803A180F24B1E3)

**Response**:

```json
{
  "tx": {
    "id": "27208951434B8D7ACB416C6B0B5DAE6449A3A3D2C4484F0B36803A180F24B1E3",
    "chain": "THOR",
    "from_address": "thor17hwqt302e5f2xm4h95ma8wuggqkvfzgvsnh5z9",
    "to_address": "thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0",
    "coins": [
      {
        "asset": "THOR.RUNE",
        "amount": "9671547414"
      }
    ],
    "gas": null,
    "memo": "=:SOL~SOL:thor17hwqt302e5f2xm4h95ma8wuggqkvfzgvsnh5z9:61140079/1/1"
  },
  "planned_out_txs": [
    {
      "chain": "THOR",
      "to_address": "thor17hwqt302e5f2xm4h95ma8wuggqkvfzgvsnh5z9",
      "coin": {
        "asset": "SOL~SOL",
        "amount": "61164077"
      },
      "refund": false
    }
  ],
  "out_txs": [
    {
      "id": "0000000000000000000000000000000000000000000000000000000000000000",
      "chain": "THOR",
      "from_address": "thor1g98cy3n9mmjrpn0sxmn63lztelera37n8n67c0",
      "to_address": "thor17hwqt302e5f2xm4h95ma8wuggqkvfzgvsnh5z9",
      "coins": [
        {
          "asset": "SOL~SOL",
          "amount": "61164077"
        }
      ],
      "gas": [
        {
          "asset": "THOR.RUNE",
          "amount": "2000000"
        }
      ],
      "memo": "OUT:27208951434B8D7ACB416C6B0B5DAE6449A3A3D2C4484F0B36803A180F24B1E3"
    }
  ],
  "stages": {
    "inbound_observed": {
      "final_count": 0,
      "completed": true
    },
    "inbound_confirmation_counted": {
      "remaining_confirmation_seconds": 0,
      "completed": true
    },
    "inbound_finalised": {
      "completed": true
    },
    "swap_status": {
      "pending": false
    },
    "swap_finalised": {
      "completed": true
    }
  }
}
```

## **Find Liquidity Position**

Looking up the liquidity position with a given address is possible.

### **Liquidity Provider Position using Thornode**

**Request**: _Get liquidity provider information in the BTC pool for the address_ bc1q00nrswtpp3zddgc0uvppuszhnr8k8zfcdps9gn [https://gateway.liquify.com/chain/thorchain_api/thorchain/pool/BTC.BTC/liquidity_provider/bc1q00nrswtpp3zddgc0uvppuszhnr8k8zfcdps9gn](https://gateway.liquify.com/chain/thorchain_api/thorchain/pool/BTC.BTC/liquidity_provider/bc1q00nrswtpp3zddgc0uvppuszhnr8k8zfcdps9gn)

**Response:**

```json
{
  "asset": "BTC.BTC",
  "asset_address": "bc1q00nrswtpp3zddgc0uvppuszhnr8k8zfcdps9gn",
  "last_add_height": 6332320,
  "units": "3190637579",
  "pending_rune": "0",
  "pending_asset": "0",
  "rune_deposit_value": "5340160943",
  "asset_deposit_value": "548543",
  "rune_redeem_value": "0",
  "asset_redeem_value": "0",
  "luvi_deposit_value": "1696309",
  "luvi_redeem_value": "0",
  "luvi_growth_pct": "-1.000000000000000000"
}
```

### **Liquidity Provider Position using Midgard**

Several endpoints exist however the member's endpoint is the most comprehensive

**Request**: Get _liquidity provider information for the address_ bc1q0kmdagyqhkzw4sgs7f0vycxw7jhexw0rl9x9as

[https://gateway.liquify.com/chain/thorchain_midgard/v2/member/thor169lfsnv2myg8yrudx4353xakq44756w9830crc](https://gateway.liquify.com/chain/thorchain_midgard/v2/member/thor169lfsnv2myg8yrudx4353xakq44756w9830crc)

**Response:**

```json
{
  "pools": [
    {
      "assetAdded": "67500000",
      "assetAddress": "bc1qw5cj49wng7jpfg2zq6ca5py7uctq4maulyc66c",
      "assetDeposit": "67500000",
      "assetPending": "0",
      "assetWithdrawn": "0",
      "dateFirstAdded": "1669373649",
      "dateLastAdded": "1669373649",
      "liquidityUnits": "466600725237",
      "pool": "BTC.BTC",
      "runeAdded": "955003804620",
      "runeAddress": "thor169lfsnv2myg8yrudx4353xakq44756w9830crc",
      "runeDeposit": "955003804620",
      "runePending": "0",
      "runeWithdrawn": "0"
    }
  ]
}
```

Any address can be used with this endpoint, e.g. bc1q0kmdagyqhkzw4sgs7f0vycxw7jhexw0rl9x9as.

[https://gateway.liquify.com/chain/thorchain_midgard/v2/member/bc1q0kmdagyqhkzw4sgs7f0vycxw7jhexw0rl9x9as](https://gateway.liquify.com/chain/thorchain_midgard/v2/member/bc1q0kmdagyqhkzw4sgs7f0vycxw7jhexw0rl9x9as)
