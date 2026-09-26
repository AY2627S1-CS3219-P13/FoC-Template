"use client";

import { useEffect, useMemo, useState } from "react";
import AuthDialog from "./auth-dialog";
import SupplierDialog from "./supplier-dialog";
import { UserApiError, userApi, type User } from "@/lib/user-api";
import {
  categories,
  duplicateActiveName,
  locations,
  sampleSuppliers,
  type Category,
  type Location,
  type Supplier,
} from "@/lib/suppliers";

type View = "browse" | "manage";

export default function Home() {
  const [suppliers, setSuppliers] = useState<Supplier[]>(sampleSuppliers);
  const [view, setView] = useState<View>("browse");
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState<Category | "All">("All");
  const [location, setLocation] = useState<Location | "All">("All");
  const [selectedId, setSelectedId] = useState<string | null>(sampleSuppliers[0]?.id ?? null);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [formOpen, setFormOpen] = useState(false);
  const [notice, setNotice] = useState("");
  const [user, setUser] = useState<User | null>(null);
  const [checkingSession, setCheckingSession] = useState(true);
  const [authOpen, setAuthOpen] = useState(false);
  const [authMode, setAuthMode] = useState<"login" | "register">("login");
  const [authNotice, setAuthNotice] = useState("");
  const [loggingOut, setLoggingOut] = useState(false);

  useEffect(() => {
    userApi.me()
      .then(({ user }) => setUser(user))
      .catch((error: unknown) => {
        if (!(error instanceof UserApiError && error.status === 401)) {
          setAuthNotice("Could not check your session. Check that the User Service is running.");
        }
      })
      .finally(() => setCheckingSession(false));
  }, []);

  async function logout() {
    setLoggingOut(true);
    setAuthNotice("");
    try {
      await userApi.logout();
      setUser(null);
      setAuthNotice("You are logged out.");
    } catch (error) {
      setAuthNotice(error instanceof UserApiError ? error.message : "Could not reach the User Service. Please retry.");
    } finally {
      setLoggingOut(false);
    }
  }

  const visible = useMemo(() => {
    const search = query.trim().toLocaleLowerCase();
    return suppliers.filter((supplier) => {
      if (view === "browse" && !supplier.active) return false;
      if (category !== "All" && supplier.category !== category) return false;
      if (location !== "All" && supplier.location !== location) return false;
      return (
        !search ||
        `${supplier.name} ${supplier.description} ${supplier.location}`.toLocaleLowerCase().includes(search)
      );
    });
  }, [suppliers, view, query, category, location]);

  const selected = visible.find((supplier) => supplier.id === selectedId) ?? visible[0] ?? null;
  const activeCount = suppliers.filter((supplier) => supplier.active).length;

  function openCreate() {
    setEditingId(null);
    setFormOpen(true);
    setNotice("");
  }

  function openEdit(supplier: Supplier) {
    setEditingId(supplier.id);
    setFormOpen(true);
    setNotice("");
  }

  function closeForm() {
    setFormOpen(false);
    setEditingId(null);
  }

  function saveSupplier(record: Supplier) {
    if (editingId) {
      setSuppliers((items) => items.map((item) => (item.id === record.id ? record : item)));
      setNotice("Supplier updated in this demo session.");
    } else {
      setSuppliers((items) => [record, ...items]);
      setNotice("Supplier added to this demo session.");
    }
    setSelectedId(record.id);
  }

  function toggleActive(supplier: Supplier) {
    if (
      !supplier.active &&
      duplicateActiveName(suppliers, supplier.name, supplier.location, supplier.id)
    ) {
      setNotice("Rename this supplier before reactivating it; an active supplier uses that name and location.");
      return;
    }
    setSuppliers((items) =>
      items.map((item) => (item.id === supplier.id ? { ...item, active: !item.active } : item)),
    );
    setNotice(supplier.active ? "Supplier deactivated in this demo session." : "Supplier reactivated in this demo session.");
  }

  return (
    <div className="site-shell">
      <header className="topbar">
        <div className="brand" aria-label="Friend on Campus">
          <span className="brand-mark">F</span>
          <span>
            <strong>Friend on Campus</strong>
            <small>Supplier showcase</small>
          </span>
        </div>
        <div className="account-actions">
          {checkingSession ? <span className="account-label">Checking session…</span> : user ? (
            <>
              <span className="account-label" title={user.email}>{user.displayName} · {user.roles.includes("admin") ? "Admin" : "User"}</span>
              <button type="button" className="secondary-button" disabled={loggingOut} onClick={logout}>{loggingOut ? "Logging out…" : "Log out"}</button>
            </>
          ) : <>
            <button type="button" className="secondary-button" onClick={() => { setAuthNotice(""); setAuthMode("login"); setAuthOpen(true); }}>Log in</button>
            <button type="button" className="primary-button" onClick={() => { setAuthNotice(""); setAuthMode("register"); setAuthOpen(true); }}>Sign up</button>
          </>}
        </div>
      </header>

      <main>
        <section className="hero">
          <div>
            <p className="eyebrow">Explore the campus</p>
            <h1>Find the right stop for your next errand.</h1>
            <p className="hero-copy">
              Browse active pickup points, explore their details, and preview how campus suppliers will be managed.
            </p>
          </div>
          <div className="hero-stats" aria-label="Catalogue summary">
            <div><strong>{activeCount}</strong><span>Active suppliers</span></div>
            <div><strong>{categories.length}</strong><span>Categories</span></div>
            <div><strong>{locations.length}</strong><span>Campus areas</span></div>
          </div>
        </section>

        <div className="demo-note" role="note">
          <span className="note-icon" aria-hidden="true">i</span>
          <p>
            <strong>Showcase data.</strong> This catalogue runs in your browser. Changes reset on refresh; Supplier Service and its admin checks are still being built.
          </p>
        </div>
        {authNotice && <p className="notice auth-notice" role="status">{authNotice}</p>}

        <section className="workspace" aria-label="Supplier catalogue">
          <div className="section-heading">
            <div>
              <p className="eyebrow">Supplier directory</p>
              <h2>{view === "browse" ? "Browse suppliers" : "Manage suppliers"}</h2>
            </div>
            <div className="view-tabs" role="group" aria-label="Catalogue view">
              <button type="button" className={view === "browse" ? "active" : ""} onClick={() => { setView("browse"); closeForm(); setNotice(""); }}>
                Browse
              </button>
              <button type="button" className={view === "manage" ? "active" : ""} onClick={() => { setView("manage"); setNotice(""); }}>
                Manage demo
              </button>
            </div>
          </div>

          <div className="controls">
            <label className="search-field">
              <span>Search</span>
              <input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Name, description or location" />
            </label>
            <label>
              <span>Category</span>
              <select value={category} onChange={(event) => setCategory(event.target.value as Category | "All")}>
                <option value="All">All categories</option>
                {categories.map((item) => <option key={item} value={item}>{item}</option>)}
              </select>
            </label>
            <label>
              <span>Location</span>
              <select value={location} onChange={(event) => setLocation(event.target.value as Location | "All")}>
                <option value="All">All locations</option>
                {locations.map((item) => <option key={item} value={item}>{item}</option>)}
              </select>
            </label>
            {view === "manage" && <button type="button" className="primary-button add-button" onClick={openCreate}>+ Add supplier</button>}
          </div>

          {notice && <p className="notice" role="status">{notice}</p>}

          <div className="content-grid">
            <div className="supplier-list">
              <div className="list-heading">
                <span>{visible.length} {visible.length === 1 ? "result" : "results"}</span>
                <span>{view === "browse" ? "Active only" : "All statuses"}</span>
              </div>
              {visible.length === 0 ? (
                <div className="empty-state">
                  <strong>No suppliers found</strong>
                  <p>Try a different search or filter.</p>
                  <button type="button" className="text-button" onClick={() => { setQuery(""); setCategory("All"); setLocation("All"); }}>Clear filters</button>
                </div>
              ) : visible.map((supplier) => (
                <button
                  type="button"
                  key={supplier.id}
                  className={`supplier-card ${selected?.id === supplier.id ? "selected" : ""}`}
                  onClick={() => setSelectedId(supplier.id)}
                  aria-pressed={selected?.id === supplier.id}
                >
                  <span className="supplier-icon" aria-hidden="true">{supplier.name.slice(0, 1).toUpperCase()}</span>
                  <span className="supplier-card-body">
                    <span className="supplier-card-title">{supplier.name}</span>
                    <span className="supplier-card-meta">{supplier.category} · {supplier.location}</span>
                    {view === "manage" && <span className={`mini-status ${supplier.active ? "is-active" : "is-inactive"}`}>{supplier.active ? "Active" : "Inactive"}</span>}
                  </span>
                  <span className="card-arrow" aria-hidden="true">→</span>
                </button>
              ))}
            </div>

            <aside className="detail-panel" aria-label="Supplier details">
              {selected ? (
                <>
                  <div className="detail-head">
                    <span className="detail-icon" aria-hidden="true">{selected.name.slice(0, 1).toUpperCase()}</span>
                    <span className={`status-pill ${selected.active ? "is-active" : "is-inactive"}`}>{selected.active ? "Active" : "Inactive"}</span>
                  </div>
                  <p className="eyebrow">Supplier details</p>
                  <h3>{selected.name}</h3>
                  <p className="detail-description">{selected.description || "[ not provided ]"}</p>
                  <dl className="detail-facts">
                    <div><dt>Category</dt><dd>{selected.category}</dd></div>
                    <div><dt>Location</dt><dd>{selected.location}</dd></div>
                    <div><dt>Opening hours</dt><dd>{selected.openingHours || "[ not provided ]"}</dd></div>
                    {view === "manage" && <>
                      <div><dt>Supplier ID</dt><dd>{selected.id}</dd></div>
                      <div><dt>Created at</dt><dd>{selected.createdAt.slice(0, 16).replace("T", " ")} UTC</dd></div>
                    </>}
                  </dl>
                  {view === "manage" && (
                    <div className="detail-actions">
                      <button type="button" className="secondary-button" onClick={() => openEdit(selected)}>Edit details</button>
                      <button type="button" className="text-button" onClick={() => toggleActive(selected)}>
                        {selected.active ? "Deactivate" : "Reactivate"}
                      </button>
                    </div>
                  )}
                </>
              ) : (
                <div className="detail-empty">Select a supplier to view its details.</div>
              )}
            </aside>
          </div>
        </section>
      </main>

      <footer className="footer">Friend on Campus · Supplier UI preview</footer>

      {authOpen && <AuthDialog initialMode={authMode} onClose={() => setAuthOpen(false)} onLogin={(nextUser) => { setUser(nextUser); setAuthOpen(false); setAuthNotice(`Logged in as ${nextUser.displayName}.`); }} />}

      {formOpen && view === "manage" && <SupplierDialog suppliers={suppliers} supplier={editingId ? suppliers.find((item) => item.id === editingId) : undefined} onSave={saveSupplier} onClose={closeForm} />}
    </div>
  );
}
