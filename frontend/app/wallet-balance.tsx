"use client";

import { useEffect, useState } from "react";

type Wallet = { userId: string; available: number; reserved: number };

export default function WalletBalance() {
  const [wallet, setWallet] = useState<Wallet | null>(null);
  const [error, setError] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/v1/wallets/me", { credentials: "include", cache: "no-store", signal: controller.signal })
      .then((response) => {
        if (!response.ok) throw new Error("wallet unavailable");
        return response.json() as Promise<Wallet>;
      })
      .then(setWallet)
      .catch(() => { if (!controller.signal.aborted) setError(true); });
    return () => controller.abort();
  }, []);

  if (error) return <span className="account-label" role="status">Credits unavailable</span>;
  if (!wallet) return <span className="account-label" role="status">Loading credits…</span>;
  return <span className="account-label" role="status">{wallet.available} credits available{wallet.reserved ? ` · ${wallet.reserved} reserved` : ""}</span>;
}
