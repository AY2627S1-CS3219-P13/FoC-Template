"use client";

import Link from "next/link";
import { FormEvent, useState } from "react";
import { UserApiError, userApi, type User } from "@/lib/user-api";

type Order = {
  id: string;
  requesterId: string;
  courierId: string | null;
  pickup: { name: string };
  delivery: { name: string };
  offeredCredits: number;
  status: string;
  createdAt: string;
};

export default function OrdersConsole({ admin }: { admin: User }) {
  const [query, setQuery] = useState("");
  const [matches, setMatches] = useState<User[] | null>(null);
  const [target, setTarget] = useState<User | null>(null);
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setTarget(null);
    setOrders(null);
    try {
      setMatches(await userApi.searchUsers(query));
    } catch (e) {
      setError(e instanceof UserApiError ? e.message : "Could not search for users. Please retry.");
    } finally {
      setBusy(false);
    }
  }

  async function load(user: User, next = 1) {
    setBusy(true);
    setError("");
    setTarget(user);
    try {
      const response = await fetch(`/api/v1/orders?${new URLSearchParams({ scope: "user", userId: user.id, page: String(next) })}`, { credentials: "include", cache: "no-store" });
      const data = await response.json();
      if (!response.ok) throw new Error(data?.error?.message);
      setOrders(data.orders);
      setHasMore(data.hasMore);
      setPage(next);
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : "Could not load orders. Please retry.");
    } finally {
      setBusy(false);
    }
  }

  return <div className="site-shell">
    <header className="topbar">
      <Link className="brand" href="/"><span className="brand-mark">F</span><span><strong>Friend on Campus</strong><small>Administration</small></span></Link>
      <div className="account-actions"><span className="account-label">{admin.displayName} · Admin</span><Link className="secondary-button admin-nav-link" href="/admin">Admin</Link></div>
    </header>
    <main className="admin-main">
      <section className="admin-heading"><p className="eyebrow">Administration</p><h1>Order lookup</h1><p>Search for a user by school email or display name to see the orders they requested or delivered.</p></section>
      <section className="admin-panel" aria-labelledby="lookup-title">
        <h2 id="lookup-title">Find a user</h2>
        <form className="admin-form" onSubmit={search}>
          <label htmlFor="order-user-query"><span>Email or display name</span><input id="order-user-query" value={query} disabled={busy} onChange={event => setQuery(event.target.value)} required /></label>
          <button className="primary-button" disabled={busy}>{busy ? "Working…" : "Search"}</button>
        </form>
        {error && <p className="form-error" role="alert">{error}</p>}
        {matches && (matches.length ? <ul className="admin-event-list">{matches.map(user => <li key={user.id}>
          <strong>{user.displayName}</strong> <span>{user.email}</span> <button className="secondary-button" disabled={busy} onClick={() => void load(user)}>View orders</button>
        </li>)}</ul> : <p>No matching users.</p>)}
        {target && orders && <div className="admin-user-result">
          <strong>Orders for {target.displayName} ({target.email})</strong>
          {orders.length ? <ul className="admin-event-list">{orders.map(order => <li key={order.id}>
            <strong>{order.status}</strong> · {order.pickup.name} → {order.delivery.name} · {order.offeredCredits} credits · {order.requesterId === target.id ? "requester" : "courier"}
            <span> {new Date(order.createdAt).toLocaleString()}</span>
          </li>)}</ul> : <p>No orders.</p>}
          {(page > 1 || hasMore) && <div className="admin-pagination"><button className="secondary-button" disabled={busy || page === 1} onClick={() => void load(target, page - 1)}>Previous</button><span>Page {page}</span><button className="secondary-button" disabled={busy || !hasMore} onClick={() => void load(target, page + 1)}>Next</button></div>}
        </div>}
      </section>
    </main>
  </div>;
}
