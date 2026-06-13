"use client";

import { api, OpenOrder } from "@/lib/api";
import { Panel, fmt, useToast } from "./ui";

// OpenOrders lists the user's resting orders with a cancel action.
export function OpenOrders({
  orders,
  loggedIn,
  onChange,
}: {
  orders: OpenOrder[];
  loggedIn: boolean;
  onChange: () => void;
}) {
  const { push } = useToast();

  async function cancel(o: OpenOrder) {
    try {
      await api.cancelOrder(o.symbol, o.order_id);
      push("Order cancelled");
      onChange();
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  return (
    <Panel title="Open Orders">
      <div className="grid grid-cols-[1fr_0.7fr_1fr_1fr_auto] gap-1 px-3 pb-1 text-xs text-gray-500">
        <span>Market</span>
        <span>Side</span>
        <span className="text-right">Price</span>
        <span className="text-right">Filled / Size</span>
        <span></span>
      </div>
      {!loggedIn && <div className="px-3 py-2 text-gray-500">Log in to see your orders</div>}
      {loggedIn && orders.length === 0 && (
        <div className="px-3 py-2 text-gray-500">No open orders</div>
      )}
      {orders.map((o) => (
        <div
          key={o.order_id}
          className="grid grid-cols-[1fr_0.7fr_1fr_1fr_auto] items-center gap-1 px-3 py-1 num text-xs"
        >
          <span className="text-gray-200">{o.symbol}</span>
          <span className={o.side === 1 ? "text-down" : "text-up"}>
            {o.side === 1 ? "Sell" : "Buy"}
          </span>
          <span className="text-right text-gray-300">{fmt(o.price)}</span>
          <span className="text-right text-gray-400">
            {fmt(o.quantity - o.remaining)} / {fmt(o.quantity)}
          </span>
          <button
            onClick={() => cancel(o)}
            className="rounded bg-panel2 px-2 py-0.5 text-gray-300 hover:bg-line"
          >
            Cancel
          </button>
        </div>
      ))}
    </Panel>
  );
}
