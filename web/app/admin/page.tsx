"use client";

import { useCallback, useEffect, useState } from "react";
import { api, Market, Withdrawal } from "@/lib/api";
import { useAuth } from "@/components/AuthProvider";
import { Panel, Button, fmt, useToast } from "@/components/ui";

export default function AdminPage() {
  const { user } = useAuth();
  const { push } = useToast();

  const [pending, setPending] = useState<Withdrawal[]>([]);
  const [markets, setMarkets] = useState<Market[]>([]);
  const [recon, setRecon] = useState<string | null>(null);

  const load = useCallback(async () => {
    if (!user?.is_admin) return;
    try {
      const [p, m] = await Promise.all([api.adminPending(), api.symbols()]);
      setPending(p.withdrawals || []);
      setMarkets(m.markets || []);
    } catch (e) {
      push((e as Error).message, "err");
    }
  }, [user, push]);

  useEffect(() => {
    load();
  }, [load]);

  if (!user) return <div className="p-8 text-center text-gray-500">Log in as an admin.</div>;
  if (!user.is_admin)
    return <div className="p-8 text-center text-down">Admin access required.</div>;

  async function decide(id: number, approve: boolean) {
    try {
      if (approve) await api.adminApprove(id);
      else await api.adminReject(id);
      push(approve ? "Withdrawal approved & broadcast" : "Withdrawal rejected");
      load();
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  async function toggleHalt(m: Market) {
    try {
      if (m.halted) await api.adminResume(m.symbol);
      else await api.adminHalt(m.symbol);
      push(`${m.symbol} ${m.halted ? "resumed" : "halted"}`);
      load();
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  async function reconcile() {
    try {
      const r = await api.adminReconcile();
      setRecon(r.ok ? "Ledger balanced ✓" : `VIOLATION: ${r.violation}`);
      push(r.ok ? "Ledger balanced" : "Ledger violation!", r.ok ? "ok" : "err");
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  return (
    <div className="mx-auto grid max-w-5xl grid-cols-1 gap-3 p-4 md:grid-cols-2">
      <Panel title="Pending Withdrawals">
        <div className="grid grid-cols-[auto_1fr_auto_auto] gap-2 px-3 pb-1 text-xs text-gray-500">
          <span>User</span>
          <span>Asset / Amount</span>
          <span></span>
          <span></span>
        </div>
        {pending.length === 0 && <div className="px-3 py-2 text-gray-500">Queue empty</div>}
        {pending.map((w) => (
          <div
            key={w.id}
            className="grid grid-cols-[auto_1fr_auto_auto] items-center gap-2 px-3 py-1 num text-xs"
          >
            <span className="text-gray-400">#{w.user_id}</span>
            <span className="text-gray-200">
              {fmt(w.amount)} {w.asset}
              <span className="ml-1 text-gray-600">→ {w.address.slice(0, 12)}…</span>
            </span>
            <Button variant="up" onClick={() => decide(w.id, true)}>
              Approve
            </Button>
            <Button variant="down" onClick={() => decide(w.id, false)}>
              Reject
            </Button>
          </div>
        ))}
      </Panel>

      <Panel title="Markets">
        {markets.map((m) => (
          <div key={m.symbol} className="flex items-center justify-between px-3 py-1 text-xs">
            <span className="text-gray-200">{m.symbol}</span>
            <span className={m.halted ? "text-down" : "text-up"}>
              {m.halted ? "halted" : "live"}
            </span>
            <Button onClick={() => toggleHalt(m)}>{m.halted ? "Resume" : "Halt"}</Button>
          </div>
        ))}
      </Panel>

      <Panel title="Reconciliation">
        <div className="space-y-2 p-3">
          <div className="text-xs text-gray-400">
            Verify the double-entry ledger has no unbalanced transactions or drifted balances.
          </div>
          <Button onClick={reconcile}>Run reconciliation</Button>
          {recon && (
            <div className={recon.startsWith("VIOLATION") ? "text-down" : "text-up"}>{recon}</div>
          )}
        </div>
      </Panel>
    </div>
  );
}
