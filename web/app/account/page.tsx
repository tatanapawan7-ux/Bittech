"use client";

import { useCallback, useEffect, useState } from "react";
import { QRCodeSVG } from "qrcode.react";
import { api, ApiKeyInfo } from "@/lib/api";
import { useAuth } from "@/components/AuthProvider";
import { Panel, Button, Field, useToast } from "@/components/ui";

export default function AccountPage() {
  const { user, refresh } = useAuth();
  if (!user) return <div className="p-8 text-center text-gray-500">Log in to manage your account.</div>;
  return (
    <div className="mx-auto grid max-w-4xl grid-cols-1 gap-3 p-4 md:grid-cols-2">
      <Panel title="Profile">
        <div className="space-y-1 p-3 text-sm">
          <Row label="Email" value={user.email} />
          <Row label="User ID" value={String(user.user_id)} />
          <Row label="Role" value={user.is_admin ? "admin" : "user"} />
          <Row label="2FA" value={user.totp_enabled ? "enabled" : "disabled"} />
        </div>
      </Panel>
      <TwoFactor enabled={user.totp_enabled} onChange={refresh} />
      <ApiKeys />
    </div>
  );
}

function TwoFactor({ enabled, onChange }: { enabled: boolean; onChange: () => void }) {
  const { push } = useToast();
  const [setup, setSetup] = useState<{ secret: string; uri: string } | null>(null);
  const [code, setCode] = useState("");

  async function begin() {
    try {
      setSetup(await api.setup2fa());
    } catch (e) {
      push((e as Error).message, "err");
    }
  }
  async function confirm() {
    try {
      await api.confirm2fa(code);
      push("2FA enabled");
      setSetup(null);
      setCode("");
      onChange();
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  return (
    <Panel title="Two-Factor Authentication">
      <div className="space-y-3 p-3">
        {enabled ? (
          <div className="text-sm text-up">2FA is enabled on this account.</div>
        ) : setup ? (
          <>
            <div className="text-xs text-gray-400">
              Scan with an authenticator app, then enter a code to confirm.
            </div>
            <div className="flex justify-center rounded bg-white p-3">
              <QRCodeSVG value={setup.uri} size={160} />
            </div>
            <div className="num break-all rounded bg-panel2 p-2 text-xs text-gray-400">
              {setup.secret}
            </div>
            <Field label="Authenticator code" value={code} onChange={setCode} />
            <Button variant="primary" onClick={confirm} className="w-full">
              Enable 2FA
            </Button>
          </>
        ) : (
          <>
            <div className="text-sm text-gray-400">
              Protect withdrawals and logins with a time-based code.
            </div>
            <Button onClick={begin}>Set up 2FA</Button>
          </>
        )}
      </div>
    </Panel>
  );
}

function ApiKeys() {
  const { push } = useToast();
  const [keys, setKeys] = useState<ApiKeyInfo[]>([]);
  const [label, setLabel] = useState("");
  const [newSecret, setNewSecret] = useState<{ keyId: string; secret: string } | null>(null);

  const load = useCallback(async () => {
    try {
      setKeys((await api.listApiKeys()).keys || []);
    } catch (e) {
      push((e as Error).message, "err");
    }
  }, [push]);

  useEffect(() => {
    load();
  }, [load]);

  async function create() {
    try {
      const r = await api.createApiKey(label);
      setNewSecret({ keyId: r.key_id, secret: r.secret });
      setLabel("");
      load();
    } catch (e) {
      push((e as Error).message, "err");
    }
  }

  return (
    <Panel title="API Keys">
      <div className="space-y-3 p-3">
        <div className="text-xs text-gray-400">
          For programmatic trading. Requests are signed with HMAC-SHA256.
        </div>
        {keys.map((k) => (
          <div key={k.key_id} className="flex justify-between num text-xs">
            <span className="text-gray-200">{k.label || "(no label)"}</span>
            <span className="text-gray-500">{k.key_id.slice(0, 14)}…</span>
          </div>
        ))}
        {keys.length === 0 && <div className="text-xs text-gray-500">No keys yet.</div>}

        {newSecret && (
          <div className="rounded border border-yellow-500/40 bg-yellow-500/10 p-2 text-xs">
            <div className="text-yellow-400">Secret shown once — copy it now:</div>
            <div className="num break-all text-gray-200">{newSecret.secret}</div>
          </div>
        )}

        <div className="flex items-end gap-2 border-t border-line pt-2">
          <div className="flex-1">
            <Field label="Label" value={label} onChange={setLabel} />
          </div>
          <Button onClick={create}>Create key</Button>
        </div>
      </div>
    </Panel>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between">
      <span className="text-gray-500">{label}</span>
      <span className="text-gray-200">{value}</span>
    </div>
  );
}
