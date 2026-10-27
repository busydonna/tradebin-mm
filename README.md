# BeeZee DEX Market Making BOT

## Details
This trading bot fills order books on the BeeZee DEX, creates small volume orders and generates volume on liquidity pools.

Requires a BZE chain on v8.2.0 or later (the bot queries halted denoms).

## Actions

```shell
./tradebin PATH_TO_CONFIG.yml                 # mm (default): order book filler + volume maker, every 30 s
./tradebin PATH_TO_CONFIG.yml cancel          # cancel all of the wallet's orders on the market
./tradebin PATH_TO_CONFIG.yml withdraw-all ADDRESS  # send every balance of the wallet to ADDRESS
./tradebin PATH_TO_CONFIG.yml lp-volume       # swap on the liquidity pool every `interval` seconds
```

`mm` and `lp-volume` are meant to run as two processes, each with its own config file.

### Order book (`mm`)
- Without `market.pool_id` the ladder starts from the book spread, the last trade or `start_price`, and only missing levels are topped up.
- With `market.pool_id` the reference price is the pool spot price, converted to the market's orientation and truncated to `step`. Every cycle the ladder is moved onto the targets: sells at `reference + step x (spread_steps/2 + i)`, buys at `reference - step x (spread_steps/2 + 1 + i)`. Own orders off the targets are cancelled in one transaction and missing levels are placed in one transaction.
- `hold_back_interval` pauses filling/repricing after a trade by someone else.

### Pool volume (`lp-volume`)
Each swap goes from the side of the wallet holding more value (valued at the pool spot price), so the inventory stays near 50/50.
The input is a random share in `[min_reserve_pct, max_reserve_pct]` of the pool reserve of that denom, capped by the wallet balance (300000 ubze always stay for fees). Swaps below 1000 micro-units are skipped. The whole balance is never swapped at once.

### Halted denoms
At the start of every cycle the bot asks the chain whether the market's or pool's denoms are halted.
- `mm`: the wallet's orders on that market are cancelled (cancelling still works on a halted market) and filling and volume are skipped.
- `lp-volume`: the swap is skipped.

A transaction refused with tradebin error 4017 (`denom is halted`) is handled the same way. Nothing is retried in a loop.

## Config file
See `config.yml.dist` for every key, and `configs/` for ready-made BZE/USDC.inj examples (`usdc-inj-orderbook.yml.dist`, `usdc-inj-lp.yml.dist`).

| Key | Meaning |
| --- | --- |
| `market.pool_id` | optional; anchors the order book to that pool's spot price and reprices every cycle |
| `liquidity_pool.min_reserve_pct` | smallest swap as a fraction of the input-side pool reserve (default 0.001) |
| `liquidity_pool.max_reserve_pct` | largest swap as a fraction of the input-side pool reserve (default 0.005, at most 0.05) |

### Wallet mnemonic
Set the `TRADEBIN_MNEMONIC` environment variable to keep the mnemonic out of the config file; it overrides `wallet.mnemonic` when set. Never commit a config file holding a mnemonic (`config.yml` is git-ignored).

```shell
read -rs TRADEBIN_MNEMONIC && export TRADEBIN_MNEMONIC
./tradebin config.yml
```

## Build and checks
```shell
GOOS=linux GOARCH=amd64 go build -o tradebin
make check   # build, vet, golangci-lint, go mod tidy diff, govulncheck, unit tests
```

### **This is experimental software, use at your own risk.**
