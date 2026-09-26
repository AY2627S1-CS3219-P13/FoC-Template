"use client";

import { useState, type FormEvent } from "react";
import {
  categories,
  duplicateActiveName,
  emptyDraft,
  locations,
  type Category,
  type Location,
  type Supplier,
  type SupplierDraft,
} from "@/lib/suppliers";

function initialDraft(supplier?: Supplier): SupplierDraft {
  return supplier ? {
    name: supplier.name,
    category: supplier.category,
    location: supplier.location,
    active: supplier.active,
    description: supplier.description,
    openingHours: supplier.openingHours,
  } : { ...emptyDraft };
}

function displayTime(iso: string) {
  return `${iso.slice(0, 16).replace("T", " ")} UTC`;
}

export default function SupplierDialog({
  suppliers,
  supplier,
  onSave,
  onClose,
}: {
  suppliers: Supplier[];
  supplier?: Supplier;
  onSave: (record: Supplier) => void;
  onClose: () => void;
}) {
  const [draft, setDraft] = useState<SupplierDraft>(() => initialDraft(supplier));
  const [saved, setSaved] = useState<Supplier | null>(null);
  const name = draft.name.trim();
  const duplicate = !saved && draft.active && !!name && !!draft.location &&
    duplicateActiveName(suppliers, name, draft.location, supplier?.id);
  const canSave = !saved && !!name && !!draft.location && !duplicate;

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canSave || !draft.location) return;
    const record: Supplier = {
      id: supplier?.id ?? crypto.randomUUID(),
      createdAt: supplier?.createdAt ?? new Date().toISOString(),
      name,
      category: draft.category,
      location: draft.location,
      active: draft.active,
      description: draft.description.trim(),
      openingHours: draft.openingHours.trim(),
    };
    onSave(record);
    setSaved(record);
  }

  function startAnother() {
    setDraft({ ...emptyDraft });
    setSaved(null);
  }

  const summaryRecord = saved ?? supplier;
  const summaryName = saved?.name ?? (name || "[ supplier name ]");
  const summaryCategory = saved?.category ?? draft.category;
  const summaryLocation = saved?.location ?? (draft.location || "[ NUS location ]");
  const summaryActive = saved?.active ?? draft.active;
  const summaryDescription = saved?.description ?? draft.description.trim();
  const summaryHours = saved?.openingHours ?? draft.openingHours.trim();

  return (
    <div className="dialog-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
      <section className="dialog supplier-dialog" role="dialog" aria-modal="true" aria-labelledby="supplier-form-title">
        <div className="supplier-dialog-header">
          <div>
            <p className="auth-overline">Supplier management · browser demo</p>
            <h2 id="supplier-form-title">{supplier ? "Edit supplier record" : "New supplier record"}</h2>
          </div>
          <button type="button" className="close-button" onClick={onClose} aria-label="Close supplier form">×</button>
        </div>

        <div className="supplier-dialog-grid">
          <form className="supplier-record-form" onSubmit={save}>
            <fieldset className="supplier-fields" disabled={!!saved}>
              <label className="supplier-field">
                <span>Name <b>*</b></span>
                <input value={draft.name} maxLength={100} required className={duplicate ? "is-invalid" : ""}
                  onChange={(event) => setDraft({ ...draft, name: event.target.value })} placeholder="Supplier name" />
              </label>
              <label className="supplier-field">
                <span>Category <b>*</b></span>
                <select value={draft.category} onChange={(event) => setDraft({ ...draft, category: event.target.value as Category })}>
                  {categories.map((item) => <option key={item} value={item}>{item.toLowerCase()}</option>)}
                </select>
                <small>Food · printing · retail · services · other</small>
              </label>
              <label className="supplier-field">
                <span>Location <b>*</b></span>
                <select value={draft.location} required className={duplicate ? "is-invalid" : ""}
                  onChange={(event) => setDraft({ ...draft, location: event.target.value as Location | "" })}>
                  <option value="">Choose a NUS location</option>
                  {locations.map((item) => <option key={item} value={item}>{item}</option>)}
                </select>
                <small>Select from the recognised demo location list.</small>
              </label>
              <div className="supplier-field">
                <span>Status</span>
                <div className="supplier-status-options" role="group" aria-label="Supplier status">
                  <button type="button" className={draft.active ? "selected" : ""} aria-pressed={draft.active}
                    onClick={() => setDraft({ ...draft, active: true })}>active</button>
                  <button type="button" className={!draft.active ? "selected" : ""} aria-pressed={!draft.active}
                    onClick={() => setDraft({ ...draft, active: false })}>inactive</button>
                </div>
                <small>New records start active.</small>
              </div>
              <label className="supplier-field">
                <span>Description <em>(optional)</em></span>
                <textarea rows={3} maxLength={500} value={draft.description}
                  onChange={(event) => setDraft({ ...draft, description: event.target.value })} placeholder="[ not provided ]" />
              </label>
              <label className="supplier-field">
                <span>Opening hours <em>(optional)</em></span>
                <textarea rows={3} maxLength={120} value={draft.openingHours}
                  onChange={(event) => setDraft({ ...draft, openingHours: event.target.value })} placeholder="[ not provided ]" />
              </label>
            </fieldset>

            {duplicate && <div className="supplier-duplicate" role="alert">
              <strong>Cannot save — duplicate name at this location</strong>
              <p>An active supplier named “{name}” already exists at {draft.location}. Change the name or pick a different location.</p>
              <small>The same name at another location, or on an inactive record, is allowed.</small>
            </div>}

            <div className="supplier-form-actions">
              {saved ? (
                <>
                  {!supplier && <button type="button" className="secondary-button" onClick={startAnother}>New supplier</button>}
                  <button type="button" className="primary-button" onClick={onClose}>Done</button>
                </>
              ) : (
                <>
                  <button type="submit" className="primary-button" disabled={!canSave}>{supplier ? "Save changes" : "Save supplier"}</button>
                  <button type="button" className="secondary-button" onClick={onClose}>Cancel</button>
                </>
              )}
            </div>
          </form>

          <aside className="supplier-save-preview" aria-label="Supplier save preview">
            <p className="auth-overline">{saved ? "After a successful save" : "Record preview"}</p>
            <div className="supplier-summary-card">
              <strong>{summaryName}</strong>
              <p>{summaryCategory.toLowerCase()} · {summaryLocation}</p>
              <dl>
                <div><dt>Supplier ID</dt><dd>{saved?.id ?? supplier?.id ?? "[ assigned on save ]"}</dd></div>
                <div><dt>Created at</dt><dd>{summaryRecord ? displayTime(summaryRecord.createdAt) : "[ assigned on save ]"}</dd></div>
                <div><dt>Status</dt><dd className={summaryActive ? "status-active" : "status-inactive"}>{summaryActive ? "active" : "inactive"}</dd></div>
                <div><dt>Description</dt><dd>{summaryDescription || "[ not provided ]"}</dd></div>
                <div><dt>Opening hours</dt><dd>{summaryHours || "[ not provided ]"}</dd></div>
              </dl>
              <small>IDs and timestamps in this preview are generated in the browser. The Supplier Service will assign them when connected.</small>
            </div>
            <div className="supplier-preview-note">Optional fields remain visible as <strong>[ not provided ]</strong> when blank. This record exists only in this browser session.</div>
          </aside>
        </div>
      </section>
    </div>
  );
}
