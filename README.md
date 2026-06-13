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
services/matching-engine/engine    event-sourced engine: WAL journal + replay recovery
services/account-service           signup/login, TOTP 2FA, sessions, API keys (HMAC)
services/ledger-service/ledger     double-entry postings, locking, trade settlement
services/wallet-service            deposits, withdrawals (2FA+allowlist+limits), custody seam
services/exchange                  trading API: orders, balances, depth, trades, WS feed
services/exchange/cmd/exchange     single-binary backend (accounts+ledger+engine+API)
libs/ratelimit                     token-bucket rate limiter (Redis + in-memory)
libs/httpx                         edge middleware: Prometheus metrics + rate limiting
web                                Next.js app: trade, wallet, account/2FA, admin (over REST/WS)
docs/openapi.yaml                  OpenAPI 3.0 spec for the public REST API
infra/db                           SQL migrations (double-entry ledger, auth)
infra/docker-compose.yml           Postgres + Redis + Redpanda for local dev
```

## Develop

The whole stack (backend + web UI + Postgres + Redis) in one command:

```bash
make demo                 # docker compose up --build  ->  UI on :3000, API on :8080
```

Or piecewise (`make help` lists everything):

```bash
make services             # start Postgres + Redis + Redpanda (Docker)
make migrate              # apply all SQL migrations (incl. seeded assets)
make test                 # full Go test suite (needs Postgres + Redis)
make run                  # run the backend (dev faucet on)
make web-install web-dev  # run the Next.js UI on :3000
```

Quick trade from the shell:

```bash
curl -X POST :8080/v1/signup -d '{"email":"me@example.com","password":"averysecurepw"}'
TOKEN=$(curl -sX POST :8080/v1/login -d '{"email":"me@example.com","password":"averysecurepw"}' | jq -r .token)
curl -X POST :8080/v1/dev/deposit -H "Authorization: Bearer $TOKEN" -d '{"asset":"USDT","amount":1000}'
curl -X POST :8080/v1/orders -H "Authorization: Bearer $TOKEN" \
  -d '{"symbol":"BTC-USDT","type":"limit","side":"buy","price":50,"qty":4}'
curl ':8080/v1/depth?symbol=BTC-USDT'
# live market feed: ws://localhost:8080/v1/ws?symbol=BTC-USDT
```

Run the web UI (proxies REST to the backend, connects the WS directly):

```bash
cd web && npm install
# backend must allow the UI origin for the WebSocket:
#   ALLOWED_WS_ORIGINS=localhost:3000 ... go run ./services/exchange/cmd/exchange
API_BASE=http://localhost:8080 npm run dev   # http://localhost:3000
```

## Status / roadmap

- [x] Phase 0 — Foundations: money type, ledger schema, order book, local infra
- [x] Phase 1 — Accounts & auth (signup, TOTP 2FA, sessions, API keys + HMAC signing)
- [x] Phase 2 — Matching engine service: sequencer + write-ahead journal + crash-recovery
      replay (file journal now; the Journal interface swaps in Redpanda for clustering)
- [x] Phase 3 — Ledger service: settlement, balance locking, idempotent replay,
      invariant checks (consumer wiring to the engine event stream lands with Phase 4)
- [x] Phase 4 — Trading API & market data: authed order placement with ledger locking,
      settlement + refunds, cancel, balances, depth/trades REST, WebSocket event feed
      (klines/TimescaleDB and API-key HMAC auth on orders still pending)
- [x] Phase 5 — Wallet/custody: deposit addresses, confirmation-gated crediting,
      withdrawal flow (2FA + allowlist + limit + operator approval) behind a Custody
      interface (mock provider; swap in Fireblocks/BitGo by implementing one interface)
- [x] Phase 6 — Web app: multi-page Next.js UI — Trade (live order book + click-to-fill,
      candlestick chart, order entry, open orders w/ cancel, balances + faucet), Wallet
      (deposit address, allowlist, 2FA withdrawals, history), Account (2FA enrollment w/ QR,
      API keys), and an Admin console (withdrawal queue, halt/resume, reconcile)
- [x] Phase 7 — Hardening: token-bucket rate limiting (Redis), Prometheus /metrics,
      HMAC API-key auth (encrypted-at-rest secrets), admin reconcile + trading halt/resume.
      Remaining for production: external security audit, KMS for keys, compliance go-live
