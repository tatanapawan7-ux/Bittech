// REST + WebSocket client for the exchange backend.
//
// REST goes through Next's /api proxy (same-origin, no CORS). The WebSocket
// connects directly to the backend (NEXT_PUBLIC_WS_BASE), whose allowed origins
// are configured via ALLOWED_WS_ORIGINS server-side.

const WS_BASE =
  process.env.NEXT_PUBLIC_WS_BASE || "ws://localhost:8080";

export type Level = { price: number; quantity: number };
export type Depth = { bids: Level[]; asks: Level[] };

export type EngineEvent = {
  type: string;
  seq: number;
  symbol: string;
  price?: number;
  qty?: number;
  side?: number; // 0 buy, 1 sell
  order_id?: string;
  maker_order_id?: string;
  taker_order_id?: string;
};

export type Balance = { asset: string; kind: string; balance: number };

const TOKEN_KEY = "bittech_token";

export function getToken(): string | null {
  if (typeof window === "undefined") return null;
  return window.localStorage.getItem(TOKEN_KEY);
}
export function setToken(t: string) {
  window.localStorage.setItem(TOKEN_KEY, t);
}
export function clearToken() {
  window.localStorage.removeItem(TOKEN_KEY);
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const token = getToken();
  if (token) headers["Authorization"] = `Bearer ${token}`;
  const res = await fetch(`/api${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : {};
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data as T;
}

export const api = {
  signup: (email: string, password: string) =>
    req<{ user_id: number }>("POST", "/v1/signup", { email, password }),
  login: (email: string, password: string, totp_code?: string) =>
    req<{ token: string }>("POST", "/v1/login", { email, password, totp_code }),
  me: () => req<{ user_id: number; email: string; totp_enabled: boolean }>("GET", "/v1/me"),
  depth: (symbol: string, levels = 20) =>
    req<Depth>("GET", `/v1/depth?symbol=${symbol}&levels=${levels}`),
  trades: (symbol: string) =>
    req<{ trades: EngineEvent[] }>("GET", `/v1/trades?symbol=${symbol}`),
  balances: () => req<{ balances: Balance[] }>("GET", "/v1/balances"),
  placeOrder: (symbol: string, type: string, side: string, price: number, qty: number) =>
    req<{ events: EngineEvent[] }>("POST", "/v1/orders", { symbol, type, side, price, qty }),
  faucet: (asset: string, amount: number) =>
    req<{ ok: boolean }>("POST", "/v1/dev/deposit", { asset, amount }),
};

// subscribe opens the market WebSocket for a symbol and invokes onEvents for
// each batch. Returns a close function.
export function subscribe(
  symbol: string,
  onEvents: (events: EngineEvent[]) => void,
  onClose?: () => void
): () => void {
  const ws = new WebSocket(`${WS_BASE}/v1/ws?symbol=${symbol}`);
  ws.onmessage = (msg) => {
    try {
      onEvents(JSON.parse(msg.data) as EngineEvent[]);
    } catch {
      /* ignore malformed frame */
    }
  };
  ws.onclose = () => onClose?.();
  return () => ws.close();
}
