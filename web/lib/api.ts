// REST + WebSocket client for the exchange backend.
//
// REST goes through Next's /api proxy (same-origin, no CORS). The WebSocket
// connects directly to the backend (NEXT_PUBLIC_WS_BASE), whose allowed origins
// are configured via ALLOWED_WS_ORIGINS server-side.

const WS_BASE = process.env.NEXT_PUBLIC_WS_BASE || "ws://localhost:8080";

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
export type User = {
  user_id: number;
  email: string;
  totp_enabled: boolean;
  is_admin: boolean;
};
export type Market = { symbol: string; halted: boolean };
export type OpenOrder = {
  order_id: string;
  symbol: string;
  side: number; // 0 buy, 1 sell
  price: number;
  quantity: number;
  remaining: number;
};
export type ApiKeyInfo = { key_id: string; label: string; disabled: boolean };
export type AllowlistEntry = { asset: string; address: string; label: string };
export type Withdrawal = {
  id: number;
  user_id?: number;
  asset: string;
  amount: number;
  address: string;
  status: string;
  tx_hash?: string;
};

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
  // --- Account ---
  signup: (email: string, password: string) =>
    req<{ user_id: number }>("POST", "/v1/signup", { email, password }),
  login: (email: string, password: string, totp_code?: string) =>
    req<{ token: string }>("POST", "/v1/login", { email, password, totp_code }),
  me: () => req<User>("GET", "/v1/me"),
  setup2fa: () => req<{ secret: string; uri: string }>("POST", "/v1/2fa/setup"),
  confirm2fa: (code: string) =>
    req<{ enabled: boolean }>("POST", "/v1/2fa/confirm", { code }),
  createApiKey: (label: string) =>
    req<{ key_id: string; secret: string }>("POST", "/v1/apikeys", { label }),
  listApiKeys: () => req<{ keys: ApiKeyInfo[] }>("GET", "/v1/apikeys"),

  // --- Market data ---
  symbols: () => req<{ markets: Market[] }>("GET", "/v1/symbols"),
  depth: (symbol: string, levels = 20) =>
    req<Depth>("GET", `/v1/depth?symbol=${symbol}&levels=${levels}`),
  trades: (symbol: string) =>
    req<{ trades: EngineEvent[] }>("GET", `/v1/trades?symbol=${symbol}`),

  // --- Trading ---
  balances: () => req<{ balances: Balance[] }>("GET", "/v1/balances"),
  openOrders: () => req<{ orders: OpenOrder[] }>("GET", "/v1/orders"),
  placeOrder: (symbol: string, type: string, side: string, price: number, qty: number) =>
    req<{ events: EngineEvent[] }>("POST", "/v1/orders", { symbol, type, side, price, qty }),
  cancelOrder: (symbol: string, order_id: string) =>
    req<{ events: EngineEvent[] }>("POST", "/v1/orders/cancel", { symbol, order_id }),
  faucet: (asset: string, amount: number) =>
    req<{ ok: boolean }>("POST", "/v1/dev/deposit", { asset, amount }),

  // --- Wallet ---
  depositAddress: (asset: string) =>
    req<{ asset: string; address: string }>("GET", `/v1/wallet/address?asset=${asset}`),
  allowlist: () => req<{ allowlist: AllowlistEntry[] }>("GET", "/v1/wallet/allowlist"),
  addAllowlist: (asset: string, address: string, label: string, totp_code: string) =>
    req<{ ok: boolean }>("POST", "/v1/wallet/allowlist", { asset, address, label, totp_code }),
  withdraw: (asset: string, address: string, amount: number, totp_code: string) =>
    req<Withdrawal>("POST", "/v1/wallet/withdraw", { asset, address, amount, totp_code }),
  myWithdrawals: () => req<{ withdrawals: Withdrawal[] }>("GET", "/v1/wallet/withdrawals"),

  // --- Admin ---
  adminPending: () => req<{ withdrawals: Withdrawal[] }>("GET", "/v1/admin/withdrawals"),
  adminApprove: (id: number) =>
    req<Withdrawal>("POST", "/v1/admin/withdrawals/approve", { id }),
  adminReject: (id: number) =>
    req<Withdrawal>("POST", "/v1/admin/withdrawals/reject", { id }),
  adminReconcile: () => req<{ ok: boolean; violation?: string }>("GET", "/v1/admin/reconcile"),
  adminHalt: (symbol: string) =>
    req<{ symbol: string; halted: boolean }>("POST", "/v1/admin/halt", { symbol }),
  adminResume: (symbol: string) =>
    req<{ symbol: string; halted: boolean }>("POST", "/v1/admin/resume", { symbol }),
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
