"use client";

import { Depth } from "@/lib/api";
import { Panel, fmt } from "./ui";

// OrderBook renders aggregated depth, asks on top (best at the spread) and bids
// below. Clicking a row sends its price to the order form via onPriceClick.
export function OrderBook({
  depth,
  onPriceClick,
}: {
  depth: Depth | null;
  onPriceClick?: (price: number) => void;
}) {
  if (!depth) return <Panel title="Order Book">Loading…</Panel>;
  const asks = [...depth.asks].slice(0, 12).reverse();
  const bids = depth.bids.slice(0, 12);
  const maxQty = Math.max(
    1,
    ...asks.map((l) => l.quantity),
    ...bids.map((l) => l.quantity)
  );
  const last = bids[0]?.price ?? asks[asks.length - 1]?.price;

  return (
    <Panel title="Order Book">
      <div className="grid grid-cols-2 px-3 pb-1 text-xs text-gray-500">
        <span>Price</span>
        <span className="text-right">Size</span>
      </div>
      <div className="flex flex-col">
        {asks.map((l, i) => (
          <Row key={`a${i}`} level={l} side="down" maxQty={maxQty} onClick={onPriceClick} />
        ))}
        <div className="num py-1 text-center text-base font-semibold text-gray-100">
          {last ? fmt(last) : "—"}
        </div>
        {bids.map((l, i) => (
          <Row key={`b${i}`} level={l} side="up" maxQty={maxQty} onClick={onPriceClick} />
        ))}
      </div>
    </Panel>
  );
}

function Row({
  level,
  side,
  maxQty,
  onClick,
}: {
  level: { price: number; quantity: number };
  side: "up" | "down";
  maxQty: number;
  onClick?: (price: number) => void;
}) {
  const pct = (level.quantity / maxQty) * 100;
  const bar = side === "up" ? "rgba(22,199,132,0.12)" : "rgba(234,57,67,0.12)";
  return (
    <div
      onClick={() => onClick?.(level.price)}
      className="relative grid cursor-pointer grid-cols-2 px-3 py-[2px] num text-xs hover:bg-line/40"
    >
      <div className="absolute inset-y-0 right-0" style={{ width: `${pct}%`, background: bar }} />
      <span className={side === "up" ? "text-up" : "text-down"}>{fmt(level.price)}</span>
      <span className="text-right text-gray-300">{fmt(level.quantity)}</span>
    </div>
  );
}
