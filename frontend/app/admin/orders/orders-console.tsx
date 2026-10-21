"use client";

import Link from "next/link";
import { FormEvent, useState } from "react";
import { UserApiError, userApi, type User } from "@/lib/user-api";

type Place = { type: string; name: string; locationName?: string; description?: string; openingHours?: string };
type Order = {
  id: string;
  requesterId: string;
  courierId: string | null;
  pickup: Place;
  delivery: Place;
  items: { name: string; quantity: number; description?: string }[];
  offeredCredits: number;
  expiresAt: string;
  status: string;
  cancellationReason?: string;
};

const sgt = (value: string) => new Intl.DateTimeFormat("en-SG", { timeZone: "Asia/Singapore", dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
const placeLabel = (p: Place) => (p.type === "supplier" ? `Supplier: ${p.name} · ${p.locationName}` : `Campus: ${p.name}`);
const placeDetails = (p: Place) => [placeLabel(p), p.description, p.openingHours && `Hours: ${p.openingHours}`].filter(Boolean).join("\n");
const Pill = ({ status }: { status: string }) => <span className={`pill status-${status.replaceAll(" ", "-")}`}>{status}</span>;

export default function OrdersConsole({ admin }: { admin: User }) {
  const [query, setQuery] = useState("");
  const [matches, setMatches] = useState<User[] | null>(null);
  const [target, setTarget] = useState<User | null>(null);
  const [orders, setOrders] = useState<Order[] | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
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
    setSelected(null);
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
      setSelected(data.orders[0]?.id ?? null);
      setHasMore(data.hasMore);
      setPage(next);
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : "Could not load orders. Please retry.");
    } finally {
      setBusy(false);
    }
  }

  const detail = orders?.find(order => order.id === selected);

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
      </section>
      {target && orders && <section className="lookup-results" aria-label={`Orders for ${target.displayName}`}>
        <p className="muted">Orders for <strong>{target.displayName}</strong> ({target.email})</p>
        <div className="lookup-grid">
          <div>
            {orders.length ? orders.map(order => <button key={order.id} className={`order-card${selected === order.id ? " selected" : ""}`} onClick={() => setSelected(order.id)}>
              <h3>{order.pickup.name} → {order.delivery.name}</h3>
              <p>{order.items.length} item{order.items.length === 1 ? "" : "s"} · {order.offeredCredits} credits</p>
              <Pill status={order.status} /><small>Expires {sgt(order.expiresAt)} SGT</small>
            </button>) : <div className="lookup-empty">No errands here yet.</div>}
            {(page > 1 || hasMore) && <div className="admin-pagination"><button className="secondary-button" disabled={busy || page === 1} onClick={() => void load(target, page - 1)}>Previous</button><span>Page {page}</span><button className="secondary-button" disabled={busy || !hasMore} onClick={() => void load(target, page + 1)}>Next</button></div>}
          </div>
          <aside className="admin-panel lookup-detail">{detail ? <>
            <p className="eyebrow">Errand details</p>
            <h3>{detail.pickup.name} → {detail.delivery.name}</h3>
            <Pill status={detail.status} />
            {[
              ["Pickup", placeDetails(detail.pickup)],
              ["Delivery", placeDetails(detail.delivery)],
              ["Items", detail.items.map(i => `${i.quantity} × ${i.name}${i.description ? `\n${i.description}` : ""}`).join("\n\n")],
              ["Offer", `${detail.offeredCredits} credits`],
              ["Expiry · Singapore time", `${sgt(detail.expiresAt)} SGT`],
              ["Role", [detail.requesterId === target.id && "Requester", detail.courierId === target.id && "Courier"].filter(Boolean).join(" and ")],
            ].map(([label, text]) => <div key={label}><h4>{label}</h4><p>{text}</p></div>)}
            {detail.cancellationReason && <p className="form-error">{detail.cancellationReason}</p>}
          </> : <><h3>Select an errand</h3><p className="muted">See the exact pickup, delivery and item details here.</p></>}</aside>
        </div>
      </section>}
    </main>
  </div>;
}
