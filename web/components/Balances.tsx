"use client";

import { useState } from "react";
import { api, Balance } from "@/lib/api";
import { Panel } from "./OrderBook";

// Balances shows the user's per-asset main/locked balances. The dev faucet
// button is shown so the demo can be funded without a custody integration.
export function Balances({
  balances,
  loggedIn,
  onChange,
}: {
  balances: Balance[];
  loggedIn: boolean;
  onChange: () => void;
}) {
  const [asset, setAsset] = useState("USDT");
  const [amount, setAmount] = useState("1000");

  async function faucet() {
    try {
      await api.faucet(asset, Number(amount));
      onChange();
    } catch {
      /* faucet disabled in prod; ignore */
    }
  }

  return (
    <Panel title="Balances">
      <div className="p-1">
        {!loggedIn && <div className="px-2 py-2 text-gray-500">Log in to see balances</div>}
        {loggedIn && balances.length === 0 && (
          <div className="px-2 py-2 text-gray-500">No balances yet — use the faucet</div>
        )}
        {balances.map((b, i) => (
          <div key={i} className="grid grid-cols-3 px-2 py-[3px] num text-xs">
            <span className="text-gray-200">{b.asset}</span>
            <span className="text-right text-gray-500">{b.kind}</span>
            <span className="text-right text-gray-200">{b.balance.toLocaleString()}</span>
          </div>
        ))}
        {loggedIn && (
          <div className="mt-2 flex items-center gap-1 border-t border-line px-2 pt-2">
            <input
              value={asset}
              onChange={(e) => setAsset(e.target.value.toUpperCase())}
              className="w-16 rounded bg-panel2 px-2 py-1 text-xs outline-none"
            />
            <input
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
              className="num w-24 rounded bg-panel2 px-2 py-1 text-xs outline-none"
            />
            <button
              onClick={faucet}
              className="rounded bg-yellow-500/20 px-2 py-1 text-xs text-yellow-400"
            >
              Faucet
            </button>
          </div>
        )}
      </div>
    </Panel>
  );
}
