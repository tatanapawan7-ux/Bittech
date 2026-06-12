"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  subscribe,
  getToken,
  Depth,
  EngineEvent,
  Balance,
} from "@/lib/api";
import { OrderBook } from "@/components/OrderBook";
import { Trades } from "@/components/Trades";
import { OrderForm } from "@/components/OrderForm";
import { Balances } from "@/components/Balances";
import { Chart } from "@/components/Chart";
import { AuthBar } from "@/components/AuthBar";

const SYMBOLS = (process.env.NEXT_PUBLIC_SYMBOLS || "BTC-USDT,ETH-USDT").split(",");

export default function TradePage() {
  const [symbol, setSymbol] = useState(SYMBOLS[0]);
  const [depth, setDepth] = useState<Depth | null>(null);
  const [trades, setTrades] = useState<EngineEvent[]>([]);
  const [balances, setBalances] = useState<Balance[]>([]);
  const [email, setEmail] = useState<string | null>(null);
  const loggedIn = email !== null;

  const refreshDepth = useCallback(async () => {
    try {
      setDepth(await api.depth(symbol));
    } catch {
      /* backend may be down; keep last snapshot */
    }
  }, [symbol]);

  const refreshBalances = useCallback(async () => {
    if (!getToken()) return;
    try {
      setBalances((await api.balances()).balances || []);
    } catch {
      /* not logged in */
    }
  }, []);

  const loadMe = useCallback(async () => {
    if (!getToken()) {
      setEmail(null);
      return;
    }
    try {
      setEmail((await api.me()).email);
      refreshBalances();
    } catch {
      setEmail(null);
    }
  }, [refreshBalances]);

  // Initial load + whenever the symbol changes: snapshot depth and trades.
  useEffect(() => {
    refreshDepth();
    api.trades(symbol).then((t) => setTrades(t.trades || [])).catch(() => {});
  }, [symbol, refreshDepth]);

  useEffect(() => {
    loadMe();
  }, [loadMe]);

  // Live feed: on any market event, append trades and refresh the book. We
  // re-fetch the depth snapshot on events rather than apply deltas client-side
  // for simplicity (the snapshot endpoint is cheap for an MVP).
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const close = subscribe(symbol, (events) => {
      const newTrades = events.filter((e) => e.type === "trade");
      if (newTrades.length) {
        setTrades((prev) => [...prev, ...newTrades].slice(-200));
        refreshBalances();
      }
      if (debounceRef.current) clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(refreshDepth, 80);
    });
    return close;
  }, [symbol, refreshDepth, refreshBalances]);

  return (
    <div className="min-h-screen">
      <header className="flex items-center justify-between border-b border-line px-4 py-2">
        <div className="flex items-center gap-4">
          <span className="text-lg font-bold text-yellow-400">Bittech</span>
          <select
            value={symbol}
            onChange={(e) => setSymbol(e.target.value)}
            className="rounded bg-panel2 px-2 py-1 text-sm outline-none"
          >
            {SYMBOLS.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </div>
        <AuthBar email={email} onAuthed={loadMe} onLogout={() => setEmail(null)} />
      </header>

      <main className="grid grid-cols-1 gap-2 p-2 lg:grid-cols-[1fr_320px_300px]">
        <section className="space-y-2">
          <div className="rounded border border-line bg-panel p-1">
            <Chart trades={trades} />
          </div>
          <Balances balances={balances} loggedIn={loggedIn} onChange={refreshBalances} />
        </section>

        <section className="space-y-2">
          <OrderBook depth={depth} />
        </section>

        <section className="space-y-2">
          <OrderForm
            symbol={symbol}
            loggedIn={loggedIn}
            onPlaced={() => {
              refreshDepth();
              refreshBalances();
            }}
          />
          <Trades trades={trades} />
        </section>
      </main>
    </div>
  );
}
