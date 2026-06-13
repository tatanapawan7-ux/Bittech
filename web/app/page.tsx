"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, subscribe, Depth, EngineEvent, Balance, OpenOrder, Market } from "@/lib/api";
import { useAuth } from "@/components/AuthProvider";
import { OrderBook } from "@/components/OrderBook";
import { Trades } from "@/components/Trades";
import { OrderForm } from "@/components/OrderForm";
import { Balances } from "@/components/Balances";
import { OpenOrders } from "@/components/OpenOrders";
import { Chart } from "@/components/Chart";
import { fmt } from "@/components/ui";

const FALLBACK = (process.env.NEXT_PUBLIC_SYMBOLS || "BTC-USDT,ETH-USDT")
  .split(",")
  .map((s) => ({ symbol: s, halted: false }));

export default function TradePage() {
  const { user } = useAuth();
  const loggedIn = !!user;

  const [markets, setMarkets] = useState<Market[]>(FALLBACK);
  const [symbol, setSymbol] = useState(FALLBACK[0].symbol);
  const [depth, setDepth] = useState<Depth | null>(null);
  const [trades, setTrades] = useState<EngineEvent[]>([]);
  const [balances, setBalances] = useState<Balance[]>([]);
  const [orders, setOrders] = useState<OpenOrder[]>([]);
  const [presetPrice, setPresetPrice] = useState<number | undefined>();

  const market = markets.find((m) => m.symbol === symbol);

  const refreshDepth = useCallback(async () => {
    try {
      setDepth(await api.depth(symbol));
    } catch {
      /* keep last snapshot */
    }
  }, [symbol]);

  const refreshPrivate = useCallback(async () => {
    if (!user) {
      setBalances([]);
      setOrders([]);
      return;
    }
    try {
      const [b, o] = await Promise.all([api.balances(), api.openOrders()]);
      setBalances(b.balances || []);
      setOrders(o.orders || []);
    } catch {
      /* ignore */
    }
  }, [user]);

  // Load markets once.
  useEffect(() => {
    api.symbols().then((m) => m.markets?.length && setMarkets(m.markets)).catch(() => {});
  }, []);

  // Snapshot on symbol change.
  useEffect(() => {
    refreshDepth();
    api.trades(symbol).then((t) => setTrades(t.trades || [])).catch(() => {});
  }, [symbol, refreshDepth]);

  useEffect(() => {
    refreshPrivate();
  }, [refreshPrivate, symbol]);

  // Live feed.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const close = subscribe(symbol, (events) => {
      const newTrades = events.filter((e) => e.type === "trade");
      if (newTrades.length) {
        setTrades((prev) => [...prev, ...newTrades].slice(-200));
      }
      if (debounceRef.current) clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(() => {
        refreshDepth();
        if (user) refreshPrivate();
      }, 80);
    });
    return close;
  }, [symbol, refreshDepth, refreshPrivate, user]);

  const lastPrice = trades.length ? trades[trades.length - 1].price : undefined;

  return (
    <div>
      <div className="flex items-center gap-4 border-b border-line px-4 py-2">
        <select
          value={symbol}
          onChange={(e) => setSymbol(e.target.value)}
          className="rounded bg-panel2 px-2 py-1 text-sm outline-none"
        >
          {markets.map((m) => (
            <option key={m.symbol} value={m.symbol}>
              {m.symbol}
            </option>
          ))}
        </select>
        <span className="num text-lg font-semibold text-gray-100">{lastPrice ? fmt(lastPrice) : "—"}</span>
        {market?.halted && (
          <span className="rounded bg-down/20 px-2 py-0.5 text-xs text-down">Trading halted</span>
        )}
      </div>

      <div className="grid grid-cols-1 gap-2 p-2 lg:grid-cols-[1fr_320px_300px]">
        <section className="space-y-2">
          <div className="rounded border border-line bg-panel p-1">
            <Chart trades={trades} />
          </div>
          <OpenOrders orders={orders} loggedIn={loggedIn} onChange={refreshPrivate} />
          <Balances balances={balances} loggedIn={loggedIn} onChange={refreshPrivate} />
        </section>

        <section className="space-y-2">
          <OrderBook depth={depth} onPriceClick={setPresetPrice} />
        </section>

        <section className="space-y-2">
          <OrderForm
            symbol={symbol}
            loggedIn={loggedIn}
            presetPrice={presetPrice}
            onPlaced={() => {
              refreshDepth();
              refreshPrivate();
            }}
          />
          <Trades trades={trades} />
        </section>
      </div>
    </div>
  );
}
