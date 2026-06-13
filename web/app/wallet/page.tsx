"use client";

import { useCallback, useEffect, useState } from "react";
import { api, AllowlistEntry, Withdrawal } from "@/lib/api";
import { useAuth } from "@/components/AuthProvider";
import { Panel, Button, Field, fmt, useToast } from "@/components/ui";

const ASSETS = ["BTC", "ETH", "USDT", "USDC"];

export default function WalletPage() {
  const { user } = useAuth();
  const { push } = useToast();

  const [depositAsset, setDepositAsset] = useState("BTC");
  const [address, setAddress] = useState<string | null>(null);
  const [allowlist, setAllowlist] = useState<AllowlistEntry[]>([]);
  const [withdrawals, setWithdrawals] = useState<Withdrawal[]>([]);

  const load = useCallback(async () => {
    if (!user) return;
    try {
      const [a, w] = await Promise.all([api.allowlist(), api.myWithdrawals()]);
      setAllowlist(a.allowlist || []);
      setWithdrawals(w.withdrawals || []);
    } catch (e) {
      push((e as Error).message, "err");
    }
  }, [user, push]);

  useEffect(() => {
    load();
  }, [load]);

  async function getAddress() {
    try {
      setAddress((await api.depositAddress(depositAsset)).address);
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  if (!user) return <Prompt />;

  return (
    <div className="mx-auto grid max-w-5xl grid-cols-1 gap-3 p-4 md:grid-cols-2">
      <Panel title="Deposit">
        <div className="space-y-2 p-2">
          <div className="flex gap-2">
            <AssetSelect value={depositAsset} onChange={setDepositAsset} />
            <Button onClick={getAddress}>Get address</Button>
          </div>
          {address && (
            <div className="rounded bg-panel2 p-2">
              <div className="text-xs text-gray-500">Send {depositAsset} to:</div>
              <div className="num break-all text-sm text-gray-100">{address}</div>
              <div className="mt-1 text-xs text-gray-500">
                Credited after on-chain confirmations.
              </div>
            </div>
          )}
        </div>
      </Panel>

      <WithdrawPanel allowlist={allowlist} onDone={load} />
      <AllowlistPanel allowlist={allowlist} onDone={load} />

      <Panel title="Withdrawal History">
        <div className="grid grid-cols-4 gap-1 px-3 pb-1 text-xs text-gray-500">
          <span>Asset</span>
          <span className="text-right">Amount</span>
          <span>Status</span>
          <span className="truncate">Tx</span>
        </div>
        {withdrawals.length === 0 && <div className="px-3 py-2 text-gray-500">None yet</div>}
        {withdrawals.map((w) => (
          <div key={w.id} className="grid grid-cols-4 gap-1 px-3 py-1 num text-xs">
            <span className="text-gray-200">{w.asset}</span>
            <span className="text-right text-gray-300">{fmt(w.amount)}</span>
            <span className={statusColor(w.status)}>{w.status}</span>
            <span className="truncate text-gray-600">{w.tx_hash || "—"}</span>
          </div>
        ))}
      </Panel>
    </div>
  );
}

function WithdrawPanel({
  allowlist,
  onDone,
}: {
  allowlist: AllowlistEntry[];
  onDone: () => void;
}) {
  const { push } = useToast();
  const [asset, setAsset] = useState("BTC");
  const [address, setAddress] = useState("");
  const [amount, setAmount] = useState("");
  const [totp, setTotp] = useState("");
  const options = allowlist.filter((a) => a.asset === asset);

  async function submit() {
    try {
      await api.withdraw(asset, address, Number(amount), totp);
      push("Withdrawal requested (pending approval)");
      setAmount("");
      setTotp("");
      onDone();
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  return (
    <Panel title="Withdraw">
      <div className="space-y-2 p-2">
        <AssetSelect value={asset} onChange={setAsset} />
        <label className="block">
          <span className="text-xs text-gray-500">Destination (allowlisted)</span>
          <select
            value={address}
            onChange={(e) => setAddress(e.target.value)}
            className="mt-1 w-full rounded bg-panel2 px-2 py-1.5 text-sm outline-none"
          >
            <option value="">Select address…</option>
            {options.map((a) => (
              <option key={a.address} value={a.address}>
                {a.label ? `${a.label} — ` : ""}
                {a.address.slice(0, 18)}…
              </option>
            ))}
          </select>
        </label>
        <Field label="Amount" value={amount} onChange={setAmount} />
        <Field label="2FA code" value={totp} onChange={setTotp} />
        <Button variant="primary" onClick={submit} className="w-full">
          Request withdrawal
        </Button>
        {options.length === 0 && (
          <div className="text-xs text-gray-500">Add an allowlisted address first.</div>
        )}
      </div>
    </Panel>
  );
}

function AllowlistPanel({
  allowlist,
  onDone,
}: {
  allowlist: AllowlistEntry[];
  onDone: () => void;
}) {
  const { push } = useToast();
  const [asset, setAsset] = useState("BTC");
  const [address, setAddress] = useState("");
  const [label, setLabel] = useState("");
  const [totp, setTotp] = useState("");

  async function add() {
    try {
      await api.addAllowlist(asset, address, label, totp);
      push("Address allowlisted");
      setAddress("");
      setLabel("");
      setTotp("");
      onDone();
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  return (
    <Panel title="Withdrawal Allowlist">
      <div className="space-y-2 p-2">
        {allowlist.map((a, i) => (
          <div key={i} className="flex justify-between num text-xs text-gray-300">
            <span>
              {a.asset} · {a.label || "—"}
            </span>
            <span className="text-gray-500">{a.address.slice(0, 16)}…</span>
          </div>
        ))}
        <div className="space-y-2 border-t border-line pt-2">
          <AssetSelect value={asset} onChange={setAsset} />
          <Field label="Address" value={address} onChange={setAddress} />
          <Field label="Label" value={label} onChange={setLabel} />
          <Field label="2FA code" value={totp} onChange={setTotp} />
          <Button onClick={add} className="w-full">
            Add to allowlist
          </Button>
        </div>
      </div>
    </Panel>
  );
}

function AssetSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="rounded bg-panel2 px-2 py-1.5 text-sm outline-none"
    >
      {ASSETS.map((a) => (
        <option key={a} value={a}>
          {a}
        </option>
      ))}
    </select>
  );
}

function statusColor(s: string): string {
  if (s === "broadcast") return "text-up";
  if (s === "rejected") return "text-down";
  return "text-gray-400";
}

function Prompt() {
  return (
    <div className="p-8 text-center text-gray-500">Log in to manage deposits and withdrawals.</div>
  );
}
