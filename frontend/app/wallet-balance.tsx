"use client";

import { useCallback, useEffect, useRef, useState } from "react";

type Wallet = { userId: string; available: number; reserved: number };

export default function WalletBalance() {
  const [wallet, setWallet] = useState<Wallet | null>(null);
  const [error, setError] = useState(false);
  const [setupPending, setSetupPending] = useState(false);
  const activeRequest = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    try {
      const response = await fetch("/api/v1/wallets/me", {
        credentials: "include", cache: "no-store", signal: controller.signal,
      });
      if (response.status === 404) {
        // Only User Service knows whether a missing wallet is still being set up.
        const account = await fetch("/api/v1/users/me", {
          credentials: "include", cache: "no-store", signal: controller.signal,
        });
        if (!account.ok) throw new Error("account status unavailable");
        const { creditSetup } = await account.json() as { creditSetup: string };
        if (creditSetup === "pending") {
          if (!controller.signal.aborted) {
            setWallet(null);
            setSetupPending(true);
            setError(false);
          }
          return;
        }
      }
      if (!response.ok) throw new Error("wallet unavailable");
      const next = await response.json() as Wallet;
      if (!controller.signal.aborted) {
        setWallet(next);
        setSetupPending(false);
        setError(false);
      }
    } catch {
      if (!controller.signal.aborted) {
        setSetupPending(false);
        setError(true);
      }
    } finally {
      if (activeRequest.current === controller) activeRequest.current = null;
    }
  }, []);

  useEffect(() => {
    const initial = window.setTimeout(() => { void refresh(); }, 0);
    const onVisible = () => { if (document.visibilityState === "visible") void refresh(); };
    const onChanged = () => { void refresh(); };
    const timer = window.setInterval(onVisible, 10_000);
    window.addEventListener("focus", onVisible);
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("foc:wallet-changed", onChanged);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
      window.removeEventListener("focus", onVisible);
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("foc:wallet-changed", onChanged);
      activeRequest.current?.abort();
    };
  }, [refresh]);

  if (error) return <span className="account-label" role="status">
    {wallet ? `${wallet.available} credits (last known)` : "Credits unavailable"}{" "}
    <button type="button" className="text-button" onClick={() => { void refresh(); }}>Retry</button>
  </span>;
  if (setupPending) return <span className="account-label" role="status">Setting up your credits…</span>;
  if (!wallet) return <span className="account-label" role="status">Loading credits…</span>;
  return <span className="account-label" role="status">{wallet.available} credits available{wallet.reserved ? ` · ${wallet.reserved} reserved` : ""}</span>;
}
