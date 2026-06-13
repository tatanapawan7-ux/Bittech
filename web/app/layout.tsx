import type { Metadata } from "next";
import "./globals.css";
import { AuthProvider } from "@/components/AuthProvider";
import { ToastProvider } from "@/components/ui";
import { Nav } from "@/components/Nav";

export const metadata: Metadata = {
  title: "Bittech — Spot Exchange",
  description: "A crypto-to-crypto spot exchange.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <ToastProvider>
          <AuthProvider>
            <Nav />
            <main className="min-h-[calc(100vh-49px)]">{children}</main>
          </AuthProvider>
        </ToastProvider>
      </body>
    </html>
  );
}
