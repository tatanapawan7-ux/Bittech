"use client";

import { EngineEvent } from "@/lib/api";
import { Panel, fmt } from "./ui";

// Trades shows the live trade tape, newest first. Taker side colours the row:
// side 0 = buy (up), 1 = sell (down).
export function Trades({ trades }: { trades: EngineEvent[] }) {
  const rows = [...trades].reverse().slice(0, 30);
  return (
    <Panel title="Trades">
      <div className="grid grid-cols-3 px-3 pb-1 text-xs text-gray-500">
        <span>Price</span>
        <span className="text-right">Size</span>
        <span className="text-right">Seq</span>
      </div>
      <div className="flex flex-col">
        {rows.length === 0 && <div className="px-3 py-2 text-gray-500">No trades yet</div>}
        {rows.map((t, i) => (
          <div key={i} className="grid grid-cols-3 px-3 py-[2px] num text-xs">
            <span className={t.side === 1 ? "text-down" : "text-up"}>{fmt(t.price)}</span>
            <span className="text-right text-gray-300">{fmt(t.qty)}</span>
            <span className="text-right text-gray-600">{t.seq}</span>
          </div>
        ))}
      </div>
    </Panel>
  );
}
