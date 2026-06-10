# Bittech — crypto-to-crypto spot exchange

A web app in the spirit of Binance / Bitkub, building toward a **crypto-to-crypto spot
exchange MVP**. This repo currently contains the **Phase 0 spine**: the core money type,
the matching-engine order book, the double-entry ledger schema, and local dev infra.

The full plan (reference architecture, phased roadmap, build-vs-buy) lives in the approved
plan document. Short version below.

## Architecture at a glance

Clients (web/mobile) → API gateway → microservices, with a small, hot **matching engine**
at the core fed by a totally ordered event log:

```
order-gateway ──(ordered commands)──▶ matching-engine ──(trade events)──▶ ledger-service
                                            │                                   │
                                            └────────────▶ market-data-service  ▼
                                                           (book, trades,    PostgreSQL
                                                            klines, WS)      (ledger)
```

Principles carried over from how real exchanges are built:

- The matching engine is an **in-memory, single-writer, deterministic** state machine
  (replayable from its input log). It does no I/O.
- **Money is integers** in each asset's smallest unit — never floating point.
- The **ledger is double-entry**; every value movement balances to zero.
- **Buy, don't build** the dangerous undifferentiated parts: custody (Fireblocks/BitGo),
  KYC/AML (Sumsub), chain analytics (Chainalysis).

## Recommended stack

Go (engine + services) · Redpanda (Kafka-compatible event log) · PostgreSQL (ledger) ·
Redis (cache/sessions/rate-limit) · TimescaleDB (market data) · Next.js + TradingView (web).

## Layout

```
libs/money                         fixed-point integer money type (no floats)
services/matching-engine/orderbook in-memory limit order book, price-time priority
infra/db                           SQL migrations (double-entry ledger)
infra/docker-compose.yml           Postgres + Redis + Redpanda for local dev
```

## Develop

```bash
# Run the test suite (money + order book).
go test ./...

# Bring up local backing services.
docker compose -f infra/docker-compose.yml up -d
```

## Status / roadmap

- [x] Phase 0 — Foundations: money type, ledger schema, order book, local infra
- [ ] Phase 1 — Accounts & auth (signup, TOTP 2FA, API keys + HMAC, rate limiting)
- [ ] Phase 2 — Matching engine service: sequencer + event sourcing + replay
- [ ] Phase 3 — Ledger service: settlement, balance locking, reconciliation
- [ ] Phase 4 — Market data & REST/WebSocket APIs (depth, trades, klines)
- [ ] Phase 5 — Wallet/custody integration (Fireblocks/BitGo)
- [ ] Phase 6 — Trading frontend (Next.js + TradingView)
- [ ] Phase 7 — Admin, observability, security & compliance hardening
