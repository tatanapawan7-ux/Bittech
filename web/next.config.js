/** @type {import('next').NextConfig} */
const API_BASE = process.env.API_BASE || "http://localhost:8080";

const nextConfig = {
  reactStrictMode: true,
  // Emit a self-contained server bundle for a small production Docker image.
  output: "standalone",
  // Proxy REST calls to the exchange backend so the browser stays same-origin
  // (no CORS). The WebSocket connects directly using NEXT_PUBLIC_WS_BASE.
  async rewrites() {
    return [{ source: "/api/:path*", destination: `${API_BASE}/:path*` }];
  },
};

module.exports = nextConfig;
