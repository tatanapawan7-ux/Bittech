"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { api, setToken } from "@/lib/api";
import { useAuth } from "./AuthProvider";
import { useToast } from "./ui";

// Nav is the global top bar: page links plus an inline auth control.
export function Nav() {
  const { user, refresh, logout } = useAuth();
  const pathname = usePathname();

  const links = [
    { href: "/", label: "Trade" },
    { href: "/wallet", label: "Wallet" },
    { href: "/account", label: "Account" },
  ];
  if (user?.is_admin) links.push({ href: "/admin", label: "Admin" });

  return (
    <header className="flex items-center justify-between border-b border-line px-4 py-2">
      <div className="flex items-center gap-5">
        <span className="text-lg font-bold text-yellow-400">Bittech</span>
        <nav className="flex items-center gap-4 text-sm">
          {links.map((l) => (
            <Link
              key={l.href}
              href={l.href}
              className={
                pathname === l.href ? "text-gray-100" : "text-gray-400 hover:text-gray-200"
              }
            >
              {l.label}
            </Link>
          ))}
        </nav>
      </div>
      {user ? (
        <div className="flex items-center gap-3 text-xs">
          <span className="text-gray-300">{user.email}</span>
          {user.is_admin && (
            <span className="rounded bg-yellow-500/20 px-2 py-0.5 text-yellow-400">admin</span>
          )}
          <button onClick={logout} className="rounded bg-panel2 px-3 py-1 text-gray-300">
            Log out
          </button>
        </div>
      ) : (
        <LoginInline onAuthed={refresh} />
      )}
    </header>
  );
}

function LoginInline({ onAuthed }: { onAuthed: () => void }) {
  const [mode, setMode] = useState<"login" | "signup">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [totp, setTotp] = useState("");
  const [needTotp, setNeedTotp] = useState(false);
  const { push } = useToast();

  async function submit() {
    try {
      if (mode === "signup") {
        await api.signup(email, password);
        push("Account created");
      }
      const { token } = await api.login(email, password, totp || undefined);
      setToken(token);
      setNeedTotp(false);
      onAuthed();
    } catch (e) {
      const m = (e as Error).message;
      if (m.toLowerCase().includes("totp")) {
        setNeedTotp(true);
        push("Enter your 2FA code", "err");
      } else {
        push(m, "err");
      }
    }
  }

  return (
    <div className="flex items-center gap-1 text-xs">
      <input
        placeholder="email"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="w-40 rounded bg-panel2 px-2 py-1 outline-none"
      />
      <input
        placeholder="password"
        type="password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        className="w-32 rounded bg-panel2 px-2 py-1 outline-none"
        onKeyDown={(e) => e.key === "Enter" && submit()}
      />
      {needTotp && (
        <input
          placeholder="2FA"
          value={totp}
          onChange={(e) => setTotp(e.target.value)}
          className="w-16 rounded bg-panel2 px-2 py-1 outline-none"
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
    </div>
  );
}
