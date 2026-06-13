"use client";

import { createContext, useContext, useState, useCallback, ReactNode } from "react";

// Panel is the standard bordered card used across the app.
export function Panel({
  title,
  right,
  children,
}: {
  title?: string;
  right?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="rounded bg-panel border border-line">
      {title && (
        <div className="flex items-center justify-between border-b border-line px-3 py-2 text-xs font-semibold text-gray-300">
          <span>{title}</span>
          {right}
        </div>
      )}
      <div className="p-1">{children}</div>
    </div>
  );
}

export function Button({
  children,
  onClick,
  variant = "default",
  disabled,
  type = "button",
  className = "",
}: {
  children: ReactNode;
  onClick?: () => void;
  variant?: "default" | "primary" | "up" | "down" | "ghost";
  disabled?: boolean;
  type?: "button" | "submit";
  className?: string;
}) {
  const styles: Record<string, string> = {
    default: "bg-panel2 text-gray-200 hover:bg-line",
    primary: "bg-yellow-500 text-black hover:bg-yellow-400",
    up: "bg-up text-black hover:opacity-90",
    down: "bg-down text-white hover:opacity-90",
    ghost: "text-gray-400 hover:text-gray-200",
  };
  return (
    <button
      type={type}
      onClick={onClick}
      disabled={disabled}
      className={`rounded px-3 py-1.5 text-xs font-semibold disabled:opacity-40 ${styles[variant]} ${className}`}
    >
      {children}
    </button>
  );
}

export function Field({
  label,
  value,
  onChange,
  type = "text",
  placeholder,
}: {
  label?: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
  placeholder?: string;
}) {
  return (
    <label className="block">
      {label && <span className="text-xs text-gray-500">{label}</span>}
      <input
        type={type}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        className="mt-1 w-full rounded bg-panel2 px-2 py-1.5 text-sm outline-none focus:ring-1 focus:ring-yellow-500"
      />
    </label>
  );
}

// --- Toast notifications ---

type Toast = { id: number; msg: string; kind: "ok" | "err" };
type ToastCtx = { push: (msg: string, kind?: "ok" | "err") => void };

const Ctx = createContext<ToastCtx>({ push: () => {} });
export const useToast = () => useContext(Ctx);

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const push = useCallback((msg: string, kind: "ok" | "err" = "ok") => {
    const id = Date.now() + Math.random();
    setToasts((t) => [...t, { id, msg, kind }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 4000);
  }, []);
  return (
    <Ctx.Provider value={{ push }}>
      {children}
      <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2">
        {toasts.map((t) => (
          <div
            key={t.id}
            className={`rounded px-4 py-2 text-sm shadow-lg ${
              t.kind === "ok" ? "bg-up/90 text-black" : "bg-down/90 text-white"
            }`}
          >
            {t.msg}
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}

// fmt renders an integer minor-unit amount as a grouped number.
export function fmt(n: number | undefined): string {
  return (n ?? 0).toLocaleString();
}
