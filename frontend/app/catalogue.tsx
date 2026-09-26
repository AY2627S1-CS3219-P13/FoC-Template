"use client";
import { useCallback, useEffect, useState, type FormEvent } from "react";
import { SupplierApiError, queryString, supplierApi, supplierError } from "@/lib/supplier-api";
import { categories, categoryLabel, displayTime, initialQuery, type CampusLocation, type CatalogueQuery, type Supplier, type SupplierDraft, type SupplierPage } from "@/lib/suppliers";
import SupplierDialog from "./supplier-dialog";
import Modal from "./modal";

type Resource = { key: string; page: SupplierPage | null; locations: CampusLocation[]; error: string };

export default function Catalogue({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [query, setQuery] = useState<CatalogueQuery>(initialQuery);
  const [search, setSearch] = useState("");
  const [revision, setRevision] = useState(0);
  const [resource, setResource] = useState<Resource | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [editing, setEditing] = useState<Supplier | null | undefined>(undefined);
  const [deleting, setDeleting] = useState<Supplier | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState("");
  const [notice, setNotice] = useState("");
  const key = `${queryString(query)}:${revision}`;
  const current = resource?.key === key ? resource : null;
  const page = current?.page;
  const locations = current?.locations ?? [];
  const selected = page?.suppliers.find(s => s.id === selectedId) ?? page?.suppliers[0];
  const onFailure = useCallback((error: unknown) => {
    if (error instanceof SupplierApiError && error.status === 401) onSessionExpired();
  }, [onSessionExpired]);

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([supplierApi.list(query, controller.signal), supplierApi.locations(controller.signal)]).then(([page, locations]) => {
      if (controller.signal.aborted) return;
      const lastPage = Math.max(1, page.totalPages);
      if (query.page > lastPage) { setQuery(q => ({ ...q, page: lastPage })); return; }
      setResource({ key, page, locations: locations.locations, error: "" });
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      onFailure(error);
      setResource({ key, page: null, locations: [], error: supplierError(error) });
    });
    return () => controller.abort();
  }, [query, key, onFailure]);

  function filter(patch: Partial<CatalogueQuery>) { setSelectedId(null); setQuery(q => ({ ...q, ...patch, page: 1 })); }
  function submitSearch(event: FormEvent) { event.preventDefault(); filter({ q: search.trim() }); }
  function reset() { setSearch(""); setQuery(initialQuery); setSelectedId(null); }
  function refresh() { setRevision(n => n + 1); }

  async function save(draft: SupplierDraft) {
    try {
      const result = editing ? await supplierApi.update(editing.id, draft) : await supplierApi.create(draft);
      setSelectedId(result.supplier.id); setNotice("Supplier saved. Your current filters still apply."); refresh();
      return result.supplier;
    } catch (error) { onFailure(error); throw error; }
  }
  async function toggle(supplier: Supplier) {
    setBusy(true); setActionError(""); setNotice("");
    try { await supplierApi.update(supplier.id, { active: !supplier.active }); setNotice(supplier.active ? "Supplier deactivated." : "Supplier reactivated."); refresh(); }
    catch (error) { onFailure(error); setActionError(supplierError(error)); }
    finally { setBusy(false); }
  }
  async function remove() {
    if (!deleting) return;
    setBusy(true); setActionError("");
    try { await supplierApi.remove(deleting.id); setDeleting(null); setSelectedId(null); setNotice("Supplier deleted from the catalogue."); refresh(); }
    catch (error) { onFailure(error); setActionError(supplierError(error)); }
    finally { setBusy(false); }
  }

  return <section className="workspace live-catalogue" aria-label="Supplier catalogue">
    <div className="section-heading"><div><p className="eyebrow">Supplier directory</p><h2>Explore campus suppliers</h2></div><div className="toolbar-actions"><button className="secondary-button" onClick={refresh} disabled={!current || busy}>Refresh</button><button className="primary-button add-button" disabled={!locations.length || busy} onClick={() => setEditing(null)}>Add supplier</button></div></div>
    <form className="catalogue-controls" onSubmit={submitSearch}>
      <label className="search-field"><span>Search suppliers</span><div className="search-control"><input type="search" maxLength={100} placeholder="Name, description or campus location" value={search} onChange={e => setSearch(e.target.value)} /><button className="primary-button" type="submit">Search</button></div></label>
      <label><span>Category</span><select value={query.category} onChange={e => filter({ category: e.target.value })}><option value="">All categories</option>{categories.map(c => <option key={c} value={c}>{categoryLabel(c)}</option>)}</select></label>
      <label><span>Location</span><select value={query.locationId} onChange={e => filter({ locationId: e.target.value })}><option value="">All locations</option>{(resource?.locations ?? []).map(l => <option key={l.id} value={l.id}>{l.name}</option>)}</select></label>
      <label><span>Status</span><select value={query.status} onChange={e => filter({ status: e.target.value as CatalogueQuery["status"] })}><option value="all">Active & inactive</option><option value="active">Active only</option><option value="inactive">Inactive only</option></select></label>
      <label><span>Sort by</span><select value={query.sort} onChange={e => filter({ sort: e.target.value as CatalogueQuery["sort"] })}><option value="name">Name</option><option value="category">Category</option><option value="location">Location</option><option value="createdAt">Date added</option></select></label>
      <label><span>Order</span><select value={query.direction} onChange={e => filter({ direction: e.target.value as CatalogueQuery["direction"] })}><option value="asc">Ascending</option><option value="desc">Descending</option></select></label>
      <label><span>Per page</span><select value={query.pageSize} onChange={e => filter({ pageSize: Number(e.target.value) })}>{[5,10,20,50].map(n => <option key={n} value={n}>{n}</option>)}</select></label>
      <button className="text-button reset-filters" type="button" onClick={reset}>Clear filters</button>
    </form>
    {notice && <p className="notice" role="status">{notice}</p>}
    {actionError && !deleting && <p className="form-error" role="alert">{actionError}</p>}
    {!current ? <p className="empty-state" role="status">Loading suppliers…</p> : current.error ? <div className="empty-state"><p role="alert">{current.error}</p><button className="secondary-button" onClick={refresh}>Retry</button></div> : page && <>
      <div className="list-heading" role="status"><span>{page.total} {page.total === 1 ? "supplier" : "suppliers"} found{query.q ? ` for “${query.q}”` : ""}</span><span>Inactive suppliers are viewable for reference</span></div>
      {page.suppliers.length === 0 ? <div className="empty-state"><h3>No suppliers found</h3><p>Try another search or clear your filters.</p><button className="secondary-button" onClick={reset}>Clear filters</button></div> : <div className="content-grid">
        <div className="supplier-list" aria-label="Supplier results">{page.suppliers.map(s => <button key={s.id} className={`supplier-card ${selected?.id === s.id ? "selected" : ""} ${s.active ? "" : "inactive-card"}`} aria-pressed={selected?.id === s.id} onClick={() => { setSelectedId(s.id); setActionError(""); }}>
          <span className="supplier-icon" aria-hidden="true">{s.name[0]}</span><span className="supplier-card-body"><strong className="supplier-card-title">{s.name}</strong><span className="supplier-card-meta">{categoryLabel(s.category)} · {locations.find(l => l.id === s.locationId)?.name ?? s.locationId}</span><span className={`mini-status ${s.active ? "is-active" : "is-inactive"}`}>{s.active ? "Active" : "Inactive"}</span></span><span className="card-arrow" aria-hidden="true">→</span>
        </button>)}</div>
        {selected && <SupplierDetails key={`${selected.id}:${revision}`} id={selected.id} locations={locations} busy={busy} onFailure={onFailure} onEdit={setEditing} onToggle={toggle} onDelete={supplier => { setActionError(""); setDeleting(supplier); }} />}
      </div>}
      <nav className="pagination" aria-label="Supplier pages"><button className="secondary-button" disabled={page.page <= 1} onClick={() => setQuery(q => ({ ...q, page: q.page - 1 }))}>Previous</button><span>Page {page.totalPages === 0 ? 0 : page.page} of {page.totalPages}</span><button className="secondary-button" disabled={page.page >= page.totalPages} onClick={() => setQuery(q => ({ ...q, page: q.page + 1 }))}>Next</button></nav>
    </>}
    {editing !== undefined && <SupplierDialog supplier={editing} locations={resource?.locations ?? []} onSave={save} onClose={() => setEditing(undefined)} />}
    {deleting && <Modal titleId="delete-title" className="confirm-dialog" busy={busy} onClose={() => setDeleting(null)}><p className="eyebrow">Remove from directory</p><h2 id="delete-title">Delete {deleting.name}?</h2><p>This supplier will disappear from the catalogue. You cannot undo this here. To hide it from new pickup choices temporarily, deactivate it instead.</p>{actionError && <p className="form-error" role="alert">{actionError}</p>}<div className="detail-actions"><button autoFocus className="secondary-button" disabled={busy} onClick={() => setDeleting(null)}>Cancel</button><button className="danger-button" disabled={busy} onClick={remove}>{busy ? "Deleting…" : "Delete supplier"}</button></div></Modal>}
  </section>;
}

function SupplierDetails({ id, locations, busy, onFailure, onEdit, onToggle, onDelete }: {
  id: string; locations: CampusLocation[]; busy: boolean; onFailure: (error: unknown) => void;
  onEdit: (supplier: Supplier) => void; onToggle: (supplier: Supplier) => void; onDelete: (supplier: Supplier) => void;
}) {
  const [supplier, setSupplier] = useState<Supplier | null>(null);
  const [error, setError] = useState("");
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    supplierApi.get(id, controller.signal).then(({ supplier }) => { if (!controller.signal.aborted) { setSupplier(supplier); setError(""); } }).catch((error: unknown) => {
      if (!controller.signal.aborted) { onFailure(error); setError(supplierError(error)); }
    });
    return () => controller.abort();
  }, [id, retry, onFailure]);
  return <aside className="detail-panel" aria-label="Supplier details">
    {error ? <><p role="alert">{error}</p><button className="secondary-button" onClick={() => setRetry(n => n+1)}>Retry details</button></> : !supplier ? <p role="status">Loading details…</p> : <>
      <div className="detail-head"><div className="detail-icon" aria-hidden="true">{supplier.name[0]}</div><span className={`status-pill ${supplier.active ? "is-active" : "is-inactive"}`}>{supplier.active ? "Active" : "Inactive"}</span></div>
      <p className="eyebrow">Supplier details</p><h3>{supplier.name}</h3><p className="detail-description">{supplier.description || "Description not provided"}</p>
      {!supplier.active && <p className="inactive-note">This supplier is inactive and unavailable for new pickups.</p>}
      <dl className="live-details"><div><dt>Category</dt><dd>{categoryLabel(supplier.category)}</dd></div><div><dt>Location</dt><dd>{locations.find(l => l.id === supplier.locationId)?.name ?? supplier.locationId}</dd></div><div><dt>Opening hours</dt><dd>{supplier.openingHours || "Not provided"}</dd></div><div><dt>Supplier ID</dt><dd>{supplier.id}</dd></div><div><dt>Added</dt><dd>{displayTime(supplier.createdAt)}</dd></div><div><dt>Updated</dt><dd>{displayTime(supplier.updatedAt)}</dd></div></dl>
      <div className="detail-actions"><button className="secondary-button" disabled={busy} onClick={() => onEdit(supplier)}>Edit supplier</button><button className="secondary-button" disabled={busy} onClick={() => onToggle(supplier)}>{supplier.active ? "Deactivate" : "Reactivate"}</button><button className="danger-button" disabled={busy} onClick={() => onDelete(supplier)}>Delete supplier</button></div>
    </>}
  </aside>;
}
