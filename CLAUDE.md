# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**tradebin-mm** is an automated market-making bot for the BeeZee DEX (Cosmos SDK chain). It fills order books with buy/sell orders and generates trading volume using configurable strategies.

## Build & Run

```bash
# Build
go build -o tradebin

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o tradebin

# Run market making (default mode)
./tradebin config.yml

# Cancel all active orders
./tradebin config.yml cancel

# Withdraw all balances to an address
./tradebin config.yml withdraw-all <destination_address>

# Liquidity pool volume (separate process)
./tradebin config.yml lp-volume

# Everything CI runs: build, vet, golangci-lint, tidy diff, govulncheck, unit tests
make check
```

## Configuration

YAML config files (see `config.yml.dist` for template, `prod_configs/` for examples). The runtime `config.yml` is gitignored since it contains the wallet mnemonic; `TRADEBIN_MNEMONIC` overrides `wallet.mnemonic`. BZE/USDC.inj examples live in `configs/*.yml.dist`. Key sections: `orders`, `market`, `volume`, `wallet`, `client`, `transaction`, `logging`.

## Architecture

The application uses a layered architecture:

**Entry point** (`main.go`) — Parses CLI args, loads config, initializes dependencies, dispatches to one of three actions: market-making (default), cancel, or withdraw.

**Orchestrator** (`app.go`) — `App.Start()` runs OrdersFiller then VolumeMaker in a 30-second loop. `ErrMarketHalted` from the filler skips the volume maker for that cycle.

**Action layer** (`app/`):
- `OrdersFiller` (`orders.go`) — Places symmetric buy/sell orders around the market spread using configurable price steps, amounts, and hold-back intervals. With `market.pool_id` it anchors to the pool spot price and reprices every cycle (cancel off-target, place missing). Halted market denoms → cancel own orders, return `ErrMarketHalted`.
- `LPVolumeMaker` (`lp_volume.go`) — One `MsgMultiSwap` per interval from the larger-value side, sized at `[min_reserve_pct, max_reserve_pct]` of the input reserve, capped by balance minus `fee_reserve` ubze; skips on halted denoms and while ubze is below `fee_reserve`.
- `VolumeMaker` (`volume.go`) — Executes volume trading with two strategies: **carousel** (random trades against existing orders) and **spread** (trades within order book spread). Supports extra-volume trades every N intervals.
- `CancelAction` (`cancel.go`) and `WithdrawAction` (`withdraw.go`) — One-shot operations for cleanup.

**Tests** — unit tests live in external `_test` packages (enforced by golangci's `testpackage`) and drive the public constructors with fakes of the interfaces each component takes (`app/fakes_test.go`). No e2e: the bot needs a live chain.

**Service layer** (`app/service/`):
- `Broadcaster` (`broadcaster.go`) — Core transaction signing and broadcasting via Cosmos SDK. Caches account sequence numbers with mutex for throughput. Handles TLS/insecure gRPC, custom Bech32 prefixes, and sequence validation on simulation errors.
- `Orders` / `Sender` — Thin wrappers that construct and broadcast `MsgCreateOrder`, `MsgCancelOrder`, and `MsgSend`.

**Data provider layer** (`app/data_provider/`) — Queries blockchain state (balances, orders, markets, history) via gRPC. Order provider uses an in-memory cache with 240-minute TTL.

**Client layer** (`app/client/`) — Thread-safe gRPC connection management with TLS support and connection pooling.

**Support packages**:
- `app/wallet/` — BIP39 mnemonic to private key derivation (BIP44 path `m/44'/118'/0'/0/0`), custom Bech32 address encoding.
- `app/cache/` — Singleton in-memory cache with TTL expiration.
- `app/lock/` — Per-key mutex locking via `sync.Map`.
- `app/internal/` — Amount formatting (trailing zero trimming, step truncation), slice utilities, dependency validation errors.

## Key Dependencies

- `github.com/bze-alphateam/bze` — BZE chain types and message definitions
- `github.com/cosmos/cosmos-sdk v0.50.15` — Cosmos SDK for transaction building/signing
- bze is pinned to the v8.2.0 tag commit as a pseudo-version (the module path has no `/v8` suffix, so `@v8.2.0` cannot be used)
- `cosmossdk.io/math` — Decimal math for price/amount calculations
- `google.golang.org/grpc` — Blockchain node communication
- `github.com/sirupsen/logrus` — Structured logging

## Important Patterns

- All config structs have `.Validate()` methods called at startup; add validation for any new config fields.
- The `Broadcaster` manages sequence numbers with a mutex (`seqMtx`); `sequenceValid` flag controls when to re-fetch from chain vs use cached value.
- `go.mod` has a replace directive: `github.com/gogo/protobuf => github.com/regen-network/protobuf` — required for Cosmos SDK compatibility.
- Halt detection: `data_provider.Halt.IsDenomHalted` (gRPC `DenomHalted`) up front, `internal.IsDenomHaltedErr` for tx refusals (error 4017; gRPC loses the type, so the message is matched).
- Never start the bot from a development machine session; the operator runs it.
- The `config.yml` file contains wallet mnemonics and must never be committed.
