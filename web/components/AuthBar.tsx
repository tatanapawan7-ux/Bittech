"use client";

import { useState } from "react";
import { api, setToken, clearToken } from "@/lib/api";

// AuthBar is a compact signup/login control in the header. On success it stores
// the bearer token and notifies the parent.
export function AuthBar({
  email,
  onAuthed,
  onLogout,
}: {
  email: string | null;
  onAuthed: () => void;
  onLogout: () => void;
}) {
  const [mode, setMode] = useState<"login" | "signup">("login");
  const [e, setE] = useState("");
  const [p, setP] = useState("");
  const [totp, setTotp] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [needTotp, setNeedTotp] = useState(false);

  async function submit() {
    setErr(null);
    try {
      if (mode === "signup") {
        await api.signup(e, p);
      }
      const { token } = await api.login(e, p, totp || undefined);
      setToken(token);
      setNeedTotp(false);
      onAuthed();
    } catch (ex) {
      const m = (ex as Error).message;
      if (m.includes("totp")) setNeedTotp(true);
      setErr(m);
    }
  }

  if (email) {
    return (
      <div className="flex items-center gap-3 text-xs">
        <span className="text-gray-300">{email}</span>
        <button
          onClick={() => {
            clearToken();
            onLogout();
          }}
          className="rounded bg-panel2 px-3 py-1 text-gray-300"
        >
          Log out
        </button>
      </div>
    );
  }

  return (
    <div className="flex items-center gap-1 text-xs">
      <input
        placeholder="email"
        value={e}
        onChange={(ev) => setE(ev.target.value)}
        className="w-40 rounded bg-panel2 px-2 py-1 outline-none"
      />
      <input
        placeholder="password"
        type="password"
        value={p}
        onChange={(ev) => setP(ev.target.value)}
        className="w-32 rounded bg-panel2 px-2 py-1 outline-none"
      />
      {needTotp && (
        <input
          placeholder="2FA"
          value={totp}
          onChange={(ev) => setTotp(ev.target.value)}
          className="num w-16 rounded bg-panel2 px-2 py-1 outline-none"
        />
      )}
      <button onClick={submit} className="rounded bg-yellow-500 px-3 py-1 font-semibold text-black">
        {mode === "login" ? "Log in" : "Sign up"}
      </button>
      <button
        onClick={() => setMode(mode === "login" ? "signup" : "login")}
        className="px-1 text-gray-400"
      >
        {mode === "login" ? "Create account" : "Have an account?"}
      </button>
      {err && <span className="text-down">{err}</span>}
    </div>
  );
}
