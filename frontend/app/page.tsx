"use client";

import { useEffect, useState, type FormEvent } from "react";
import Link from "next/link";

type User = { id: string; displayName: string; roles: string[] };
type Supplier = { id: string; name: string; category: string; locationId: string; description: string };
type Location = { id: string; name: string };

class APIError extends Error {
  constructor(public status: number, message: string) { super(message); }
}

async function api<T>(path: string, method = "GET", body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method, credentials: "include", cache: "no-store",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = response.status === 204 ? undefined : await response.json().catch(() => undefined);
  if (!response.ok) throw new APIError(response.status, data?.error?.message ?? "The service is unavailable. Please retry.");
  return data as T;
}

async function catalogue() {
  const [suppliers, locations] = await Promise.all([
    api<{ suppliers: Supplier[] }>("/api/v1/suppliers"),
    api<{ locations: Location[] }>("/api/v1/locations"),
  ]);
  return { suppliers: suppliers.suppliers, locations: locations.locations };
}

export default function Home() {
  const [user, setUser] = useState<User | null>(null);
  const [suppliers, setSuppliers] = useState<Supplier[]>([]);
  const [locations, setLocations] = useState<Location[]>([]);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    let mounted = true;
    async function restore() {
      try {
        const { user } = await api<{ user: User }>("/api/v1/users/me");
        const data = await catalogue();
        if (mounted) { setUser(user); setSuppliers(data.suppliers); setLocations(data.locations); }
      } catch (err) {
        if (mounted && !(err instanceof APIError && err.status === 401)) setError("Could not connect. Refresh to try again.");
      } finally { if (mounted) setBusy(false); }
    }
    void restore();
    return () => { mounted = false; };
  }, []);

  function failed(err: unknown) {
    if (err instanceof APIError && err.status === 401) { setUser(null); setSuppliers([]); setLocations([]); }
    setError(err instanceof Error ? err.message : "The request failed. Please retry.");
  }

  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    setBusy(true); setError(""); setNotice("");
    try {
      const { user } = await api<{ user: User }>("/api/v1/auth/login", "POST", {
        email: values.get("email"), password: values.get("password"),
      });
      form.reset();
      const data = await catalogue();
      setUser(user); setSuppliers(data.suppliers); setLocations(data.locations);
    } catch (err) { failed(err); }
    finally { setBusy(false); }
  }

  async function logout() {
    setBusy(true); setError(""); setNotice("");
    try {
      await api("/api/v1/auth/logout", "POST");
      setUser(null); setSuppliers([]); setLocations([]);
    } catch (err) { failed(err); }
    finally { setBusy(false); }
  }

  async function refresh() {
    setBusy(true); setError("");
    try {
      const data = await catalogue();
      setSuppliers(data.suppliers); setLocations(data.locations);
    } catch (err) { failed(err); }
    finally { setBusy(false); }
  }

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const values = new FormData(form);
    setBusy(true); setError(""); setNotice("");
    try {
      await api("/api/v1/suppliers", "POST", Object.fromEntries(values));
      form.reset();
      setNotice("Supplier added.");
      const data = await catalogue();
      setSuppliers(data.suppliers); setLocations(data.locations);
    } catch (err) { failed(err); }
    finally { setBusy(false); }
  }

  async function deactivate(id: string) {
    setBusy(true); setError(""); setNotice("");
    try {
      await api(`/api/v1/suppliers/${id}`, "PATCH", { active: false });
      setSuppliers(items => items.filter(item => item.id !== id));
      setNotice("Supplier deactivated.");
    } catch (err) { failed(err); }
    finally { setBusy(false); }
  }

  const admin = user?.roles.includes("admin");
  return (
    <main>
      <header>
        <Link className="brand" href="/">Friend on Campus<span>Campus errands, together.</span></Link>
        {user && <div className="account"><span>{user.displayName}{admin ? " · Admin" : ""}</span><button disabled={busy} onClick={logout}>Log out</button></div>}
      </header>
      <section className="intro"><p className="eyebrow">PICKUP LOCATIONS</p><h1>Your campus, a little closer.</h1><p>Find stores and facilities for your next campus errand.</p></section>
      {error && <p role="alert" className="message error">{error}</p>}
      {notice && <p role="status" className="message">{notice}</p>}
      {!user ? (
        <section className="panel login">
          <h2>Log in to browse</h2><p>Use your verified Friend on Campus account.</p>
          <form onSubmit={login}><fieldset disabled={busy}>
            <label>School email<input name="email" type="email" autoComplete="username" required /></label>
            <label>Password<input name="password" type="password" autoComplete="current-password" required /></label>
            <button className="primary" type="submit">{busy ? "Connecting…" : "Log in"}</button>
          </fieldset></form>
        </section>
      ) : (
        <>
          {admin && <section className="panel"><h2>Add a supplier</h2><form onSubmit={create}><fieldset disabled={busy} className="create-grid">
            <label>Name<input name="name" maxLength={100} required /></label>
            <label>Category<select name="category">{["food", "printing", "retail", "services", "other"].map(category => <option key={category}>{category}</option>)}</select></label>
            <label>Location<select name="locationId" required>{locations.map(location => <option key={location.id} value={location.id}>{location.name}</option>)}</select></label>
            <label>Pickup details<input name="description" maxLength={500} placeholder="Optional floor or directions" /></label>
            <button className="primary" type="submit">Add supplier</button>
          </fieldset></form></section>}
          <div className="list-title"><h2>Active suppliers <span>({suppliers.length})</span></h2><button onClick={refresh} disabled={busy}>{busy ? "Loading…" : "Refresh"}</button></div>
          {suppliers.length === 0 && <p className="panel">No active suppliers are available yet.</p>}
          <div className="cards">{suppliers.map(supplier => <article className="panel" key={supplier.id}>
            <span className="category">{supplier.category}</span><h3>{supplier.name}</h3>
            <p className="location">{locations.find(location => location.id === supplier.locationId)?.name ?? supplier.locationId}</p>
            <p>{supplier.description}</p>
            {admin && <button className="subtle" disabled={busy} onClick={() => deactivate(supplier.id)}>Deactivate</button>}
          </article>)}</div>
        </>
      )}
      <footer>Friend on Campus · NUS</footer>
    </main>
  );
}
