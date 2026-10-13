"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { CreditApiError, creditApi, type Transaction } from "@/lib/credit-api";

const labels: Record<Transaction["kind"], string> = {
  allocation: "Initial credits", reserve: "Credits frozen", release: "Credits released",
  spend: "Errand payment", receive: "Courier earnings", admin_debit: "Admin debit",
};
const signed = (amount: number) => `${amount > 0 ? "+" : ""}${amount}`;
const descriptions: Record<Transaction["kind"], string> = {
  allocation: "Your starting campus credits", reserve: "Held for an open errand",
  release: "Returned to your available balance", spend: "Paid to your courier",
  receive: "Earned from a completed delivery", admin_debit: "Credits removed by an administrator",
};

export default function WalletHistory({ onSessionExpired, onRefreshBalance }: { onSessionExpired: () => void; onRefreshBalance: () => Promise<void> }) {
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [nextBefore, setNextBefore] = useState<number | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const activeRequest = useRef<AbortController | null>(null);
  const requestedBefore = useRef<number | null>(null);

  const load = useCallback(async (before: number | null) => {
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    requestedBefore.current = before;
    setLoading(true);
    setError("");
    try {
      const page = await creditApi.transactions(before, controller.signal);
      if (controller.signal.aborted) return;
      setTransactions((previous) => before === null ? page.transactions : [
        ...previous, ...page.transactions.filter((entry) => !previous.some((existing) => existing.id === entry.id)),
      ]);
      setNextBefore(page.nextBefore);
      setLoaded(true);
    } catch (error: unknown) {
      if (controller.signal.aborted) return;
      if (error instanceof CreditApiError && error.status === 401) { onSessionExpired(); return; }
      setError("Could not load transactions. Please retry.");
    } finally {
      if (activeRequest.current === controller) { activeRequest.current = null; setLoading(false); }
    }
  }, [onSessionExpired]);

  useEffect(() => {
    const initial = window.setTimeout(() => { void load(null); }, 0);
    const onChanged = () => { void load(null); };
    window.addEventListener("foc:wallet-changed", onChanged);
    return () => {
      window.clearTimeout(initial);
      window.removeEventListener("foc:wallet-changed", onChanged);
      activeRequest.current?.abort();
    };
  }, [load]);

  return <section className="wallet-history" aria-labelledby="wallet-history-title" aria-busy={loading}>
    <div className="wallet-history-heading"><div><p className="eyebrow">Credit activity</p><h2 id="wallet-history-title">Recent transactions</h2><p className="wallet-history-copy">Your latest activity, newest first.</p></div>
      <button className="secondary-button" disabled={loading} onClick={() => { void load(null); void onRefreshBalance(); }}>Refresh</button></div>
    {error && <p className="form-error" role="alert">{error}{" "}
      <button className="text-button" disabled={loading} onClick={() => { void load(requestedBefore.current); }}>Retry</button></p>}
    {loaded && transactions.length === 0 && <div className="wallet-empty"><strong>No transactions yet.</strong><p>Your credit activity will appear here when your balance changes.</p></div>}
    {transactions.length > 0 && <div className="wallet-list-labels" aria-hidden="true"><span>Transaction</span><span>Credit movement</span><span>Balance after</span></div>}
    <ol className="wallet-transactions">
      {transactions.map((entry) => <li key={entry.id}>
        <div className="wallet-transaction-detail">
          <span className={`wallet-transaction-icon wallet-kind-${entry.kind}`} aria-hidden="true">{entry.kind === "reserve" || entry.kind === "release" ? "↔" : entry.availableDelta + entry.reservedDelta > 0 ? "↙" : "↗"}</span>
          <div><strong>{labels[entry.kind] ?? "Credit transaction"}</strong><p>{descriptions[entry.kind]}</p>
            <time dateTime={entry.createdAt}>{new Date(entry.createdAt).toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "numeric", minute: "2-digit" })}</time>
            {entry.orderId && <p className="wallet-order">Order: {entry.orderId}</p>}
          </div>
        </div>
        <div className="wallet-transaction-movement"><strong className={entry.availableDelta + entry.reservedDelta > 0 ? "wallet-positive" : ""}>{entry.availableDelta + entry.reservedDelta === 0 ? entry.amount : signed(entry.availableDelta + entry.reservedDelta)} credits</strong>
          <p>Available: {signed(entry.availableDelta)}</p><p>Frozen: {signed(entry.reservedDelta)}</p>
        </div>
        <div className="wallet-transaction-balance"><span className="wallet-mobile-label">Balance after</span><strong>{entry.availableAfter} available</strong><p>{entry.reservedAfter} frozen</p></div>
      </li>)}
    </ol>
    <div className="wallet-history-footer">
      <p role="status">{loading ? "Loading transactions…" : loaded && transactions.length > 0 ? `Showing ${transactions.length} transaction${transactions.length === 1 ? "" : "s"}` : ""}</p>
      {loaded && nextBefore !== null && <button className="secondary-button" disabled={loading} onClick={() => { void load(nextBefore); }}>Show more</button>}
    </div>
  </section>;
}
