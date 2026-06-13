"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Panel, useToast } from "./ui";

// OrderForm places limit/market orders. `presetPrice` lets the order book push
// a clicked price in; onPlaced refreshes the parent after a successful order.
export function OrderForm({
  symbol,
  loggedIn,
  presetPrice,
  onPlaced,
}: {
  symbol: string;
  loggedIn: boolean;
  presetPrice?: number;
  onPlaced: () => void;
}) {
  const [side, setSide] = useState<"buy" | "sell">("buy");
  const [type, setType] = useState<"limit" | "market">("limit");
  const [price, setPrice] = useState("");
  const [qty, setQty] = useState("");
  const [busy, setBusy] = useState(false);
  const { push } = useToast();

  // When the user clicks a price in the order book, fill it here.
  useEffect(() => {
    if (presetPrice && presetPrice > 0) setPrice(String(presetPrice));
  }, [presetPrice]);

  const [base, quote] = symbol.split("-");
  const total = type === "limit" && price && qty ? Number(price) * Number(qty) : 0;

  async function submit() {
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
      push(filled ? `Filled in ${filled} trade(s)` : "Order resting on book");
      setQty("");
      onPlaced();
    } catch (e) {
      push((e as Error).message, "err");
    } finally {
      setBusy(false);
    }
  }

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
        <div className="flex gap-3 text-xs">
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
        {type === "limit" && <Input label={`Price (${quote})`} value={price} onChange={setPrice} />}
        <Input label={`Amount (${base})`} value={qty} onChange={setQty} />
        {total > 0 && (
          <div className="num text-xs text-gray-500">
            Total ≈ {total.toLocaleString()} {quote}
          </div>
        )}
        {type === "market" && side === "buy" && (
          <div className="text-xs text-down">Market buys aren&apos;t supported yet — use a limit order.</div>
        )}
        <button
          disabled={busy || !loggedIn}
          onClick={submit}
          className={`w-full rounded py-2 text-sm font-semibold disabled:opacity-40 ${
            side === "buy" ? "bg-up text-black" : "bg-down text-white"
          }`}
        >
          {loggedIn ? `${side === "buy" ? "Buy" : "Sell"} ${base}` : "Log in to trade"}
        </button>
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

function Input({
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
