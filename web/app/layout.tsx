import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Bittech — Spot Exchange",
  description: "A crypto-to-crypto spot exchange.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
