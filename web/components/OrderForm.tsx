"use client";

import { useState } from "react";
import { api } from "@/lib/api";
import { Panel } from "./OrderBook";

// OrderForm places limit/market orders. On success it calls onPlaced so the
// parent can refresh balances and depth.
export function OrderForm({
  symbol,
  loggedIn,
  onPlaced,
}: {
  symbol: string;
  loggedIn: boolean;
  onPlaced: () => void;
}) {
  const [side, setSide] = useState<"buy" | "sell">("buy");
  const [type, setType] = useState<"limit" | "market">("limit");
  const [price, setPrice] = useState("");
  const [qty, setQty] = useState("");
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    setMsg(null);
    setBusy(true);
    try {
      const res = await api.placeOrder(
        symbol,
        type,
        side,
        type === "limit" ? Number(price) : 0,
        Number(qty)
      );
      const filled = res.events.filter((e) => e.type === "trade").length;
      setMsg(filled ? `Filled in ${filled} trade(s)` : "Order resting on book");
      setQty("");
      onPlaced();
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const [base, quote] = symbol.split("-");

  return (
    <Panel title="Place Order">
      <div className="space-y-2 p-2">
        <div className="grid grid-cols-2 gap-1">
          <Tab active={side === "buy"} onClick={() => setSide("buy")} color="up">
            Buy {base}
          </Tab>
          <Tab active={side === "sell"} onClick={() => setSide("sell")} color="down">
            Sell {base}
          </Tab>
        </div>
        <div className="flex gap-2 text-xs">
          {(["limit", "market"] as const).map((t) => (
            <button
              key={t}
              onClick={() => setType(t)}
              className={type === t ? "text-yellow-400" : "text-gray-500"}
            >
              {t[0].toUpperCase() + t.slice(1)}
            </button>
          ))}
        </div>
        {type === "limit" && (
          <Field label={`Price (${quote})`} value={price} onChange={setPrice} />
        )}
        <Field label={`Amount (${base})`} value={qty} onChange={setQty} />
        <button
          disabled={busy || !loggedIn}
          onClick={submit}
          className={`w-full rounded py-2 text-sm font-semibold disabled:opacity-40 ${
            side === "buy" ? "bg-up text-black" : "bg-down text-white"
          }`}
        >
          {loggedIn ? `${side === "buy" ? "Buy" : "Sell"} ${base}` : "Log in to trade"}
        </button>
        {msg && <div className="text-xs text-gray-400">{msg}</div>}
      </div>
    </Panel>
  );
}

function Tab({
  active,
  onClick,
  color,
  children,
}: {
  active: boolean;
  onClick: () => void;
  color: "up" | "down";
  children: React.ReactNode;
}) {
  return (
    <button
      onClick={onClick}
      className={`rounded py-1 text-xs font-semibold ${
        active
          ? color === "up"
            ? "bg-up/20 text-up"
            : "bg-down/20 text-down"
          : "bg-panel2 text-gray-400"
      }`}
    >
      {children}
    </button>
  );
}

function Field({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <label className="block">
      <span className="text-xs text-gray-500">{label}</span>
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        inputMode="numeric"
        className="num mt-1 w-full rounded bg-panel2 px-2 py-1 outline-none focus:ring-1 focus:ring-yellow-500"
        placeholder="0"
      />
    </label>
  );
}
