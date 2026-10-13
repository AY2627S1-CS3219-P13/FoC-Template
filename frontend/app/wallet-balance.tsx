"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import WalletHistory from "./wallet-history";
import { CreditApiError, creditApi, type Wallet } from "@/lib/credit-api";

export default function WalletBalance({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [wallet, setWallet] = useState<Wallet | null>(null);
  const [error, setError] = useState(false);
  const [setupPending, setSetupPending] = useState(false);
  const activeRequest = useRef<AbortController | null>(null);

  const refresh = useCallback(async () => {
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    try {
      let next: Wallet;
      try { next = await creditApi.wallet(controller.signal); }
      catch (error: unknown) {
        if (!(error instanceof CreditApiError) || error.status !== 404) throw error;
        // Only User Service knows whether a missing wallet is still being set up.
        const account = await fetch("/api/v1/users/me", {
          credentials: "include", cache: "no-store", signal: controller.signal,
        });
        if (account.status === 401) throw new CreditApiError(401, "Session expired");
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
        throw error;
      }
      if (!controller.signal.aborted) {
        setWallet(next);
        setSetupPending(false);
        setError(false);
      }
    } catch (error: unknown) {
      if (!controller.signal.aborted) {
        if (error instanceof CreditApiError && error.status === 401) { onSessionExpired(); return; }
        setSetupPending(false);
        setError(true);
      }
    } finally {
      if (activeRequest.current === controller) activeRequest.current = null;
    }
  }, [onSessionExpired]);

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

  const balance = error ? <p className="form-error" role="status">
    {wallet ? `${wallet.available} credits (last known)` : "Credits unavailable"}{" "}
    <button type="button" className="text-button" onClick={() => { void refresh(); }}>Retry</button>
  </p> : setupPending ? <p className="notice" role="status">Setting up your credits…</p> :
    !wallet ? <p className="notice" role="status">Loading credits…</p> : null;

  return <div className="wallet-content">
      {balance}
      {wallet && <section className={`wallet-balances${error ? " wallet-stale" : ""}`} aria-label={error ? "Last known balances" : "Current balances"}>
        <div className="wallet-balance-card wallet-available"><p className="eyebrow">Available credits</p><p className="wallet-balance-value">{wallet.available.toLocaleString()} <span>credits</span></p><p>Ready for your next campus errand.</p></div>
        <div className="wallet-balance-card"><p className="eyebrow">Frozen credits</p><p className="wallet-balance-value">{wallet.reserved.toLocaleString()} <span>credits</span></p><p>Reserved for your open errands. Unavailable to spend until released.</p></div>
        <div className="wallet-balance-card"><p className="eyebrow">Total balance</p><p className="wallet-balance-value">{(wallet.available + wallet.reserved).toLocaleString()} <span>credits</span></p><p>Your available and frozen credits combined.</p></div>
      </section>}
      {wallet && <WalletHistory onSessionExpired={onSessionExpired} onRefreshBalance={refresh} />}
  </div>;
}
