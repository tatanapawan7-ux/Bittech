"use client";

import { Depth } from "@/lib/api";

// scale converts integer minor units to a display number. The MVP uses raw
// integer prices/quantities, so we show them as-is; wire per-asset scale here
// when assets carry precision metadata.
function fmt(n: number): string {
  return n.toLocaleString();
}

export function OrderBook({ depth }: { depth: Depth | null }) {
  if (!depth) return <Panel title="Order Book">Loading…</Panel>;
  const asks = [...depth.asks].slice(0, 12).reverse();
  const bids = depth.bids.slice(0, 12);
  const maxQty = Math.max(
    1,
    ...asks.map((l) => l.quantity),
    ...bids.map((l) => l.quantity)
  );

  return (
    <Panel title="Order Book">
      <Header />
      <div className="flex flex-col">
        {asks.map((l, i) => (
          <Row key={`a${i}`} level={l} side="down" maxQty={maxQty} />
        ))}
        <div className="num py-1 text-center text-base font-semibold text-up">
          {bids[0] ? fmt(bids[0].price) : asks.length ? fmt(asks[asks.length - 1].price) : "—"}
        </div>
        {bids.map((l, i) => (
          <Row key={`b${i}`} level={l} side="up" maxQty={maxQty} />
        ))}
      </div>
    </Panel>
  );
}

function Row({
  level,
  side,
  maxQty,
}: {
  level: { price: number; quantity: number };
  side: "up" | "down";
  maxQty: number;
}) {
  const pct = (level.quantity / maxQty) * 100;
  const bar = side === "up" ? "rgba(22,199,132,0.12)" : "rgba(234,57,67,0.12)";
  return (
    <div className="relative grid grid-cols-2 px-3 py-[2px] num text-xs">
      <div
        className="absolute inset-y-0 right-0"
        style={{ width: `${pct}%`, background: bar }}
      />
      <span className={side === "up" ? "text-up" : "text-down"}>{fmt(level.price)}</span>
      <span className="text-right text-gray-300">{fmt(level.quantity)}</span>
    </div>
  );
}

function Header() {
  return (
    <div className="grid grid-cols-2 px-3 pb-1 text-xs text-gray-500">
      <span>Price</span>
      <span className="text-right">Size</span>
    </div>
  );
}

export function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded bg-panel border border-line">
      <div className="border-b border-line px-3 py-2 text-xs font-semibold text-gray-300">
        {title}
      </div>
      <div className="p-1">{children}</div>
    </div>
  );
}
