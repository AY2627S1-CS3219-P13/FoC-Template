"use client";
import { useState, type ChangeEvent, type FormEvent } from "react";
import { categories, categoryLabel, displayTime, emptyDraft, type CampusLocation, type Category, type Supplier, type SupplierDraft } from "@/lib/suppliers";
import { supplierError } from "@/lib/supplier-api";
import { photoData } from "@/lib/image";
import Modal from "./modal";

export default function SupplierDialog({ supplier, locations, onSave, onClose }: {
  supplier: Supplier | null; locations: CampusLocation[]; onSave: (draft: SupplierDraft) => Promise<Supplier>; onClose: () => void;
}) {
  const [draft, setDraft] = useState<SupplierDraft>(supplier ? {
    name: supplier.name, category: supplier.category, locationId: supplier.locationId,
    description: supplier.description, openingHours: supplier.openingHours, active: supplier.active,
  } : emptyDraft);
  const [saved, setSaved] = useState<Supplier | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  // The photo is sent only when it changed; an empty string removes it.
  const originalImage = supplier?.image ?? "";
  const [image, setImage] = useState(originalImage);
  const [preparing, setPreparing] = useState(false);
  const [imageError, setImageError] = useState("");
  const summary = saved ?? draft;
  const previewImage = saved ? saved.image : image;
  async function chooseImage(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    setPreparing(true); setImageError("");
    try { setImage(await photoData(file)); }
    catch (failure) { setImageError(failure instanceof Error ? failure.message : "This photo could not be prepared."); }
    finally { setPreparing(false); }
  }
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError("");
    const payload: SupplierDraft = { ...draft, name: draft.name.trim(), description: draft.description.trim(), openingHours: draft.openingHours.trim() };
    if (image !== originalImage) payload.image = image;
    try { setSaved(await onSave(payload)); }
    catch (failure) { setError(supplierError(failure)); }
    finally { setBusy(false); }
  }
  return <Modal titleId="supplier-title" className="supplier-dialog" busy={busy} onClose={onClose}>
    <div className="dialog-heading supplier-dialog-header"><div><p className="auth-overline">Supplier directory</p><h2 id="supplier-title">{saved ? "Supplier saved" : supplier ? "Edit supplier" : "New supplier"}</h2></div><button className="close-button" aria-label="Close supplier form" disabled={busy} onClick={onClose}>×</button></div>
    <div className="supplier-dialog-grid">
      <form className="supplier-record-form" onSubmit={submit}>
        <fieldset className="supplier-fields" disabled={busy || !!saved}>
          <label className="supplier-field"><span>Name *</span><input autoFocus required maxLength={100} value={draft.name} onChange={e => setDraft({ ...draft, name: e.target.value })} /></label>
          <label className="supplier-field"><span>Category *</span><select value={draft.category} onChange={e => setDraft({ ...draft, category: e.target.value as Category })}>{categories.map(c => <option key={c} value={c}>{categoryLabel(c)}</option>)}</select></label>
          <label className="supplier-field"><span>Location *</span><select required value={draft.locationId} onChange={e => setDraft({ ...draft, locationId: e.target.value })}><option value="">Choose a campus location</option>{locations.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}</select><small>Choose a recognised campus building or landmark.</small></label>
          <label className="supplier-field"><span>Status</span><select aria-label="Status" value={draft.active ? "active" : "inactive"} onChange={e => setDraft({ ...draft, active: e.target.value === "active" })}><option value="active">Active</option><option value="inactive">Inactive</option></select><small>Inactive suppliers remain visible for reference.</small></label>
          <label className="supplier-field"><span>Description <em>(optional)</em></span><textarea rows={4} maxLength={500} value={draft.description} onChange={e => setDraft({ ...draft, description: e.target.value })} placeholder="Pickup directions or useful details" /></label>
          <label className="supplier-field"><span>Opening hours <em>(optional)</em></span><textarea rows={4} maxLength={200} value={draft.openingHours} onChange={e => setDraft({ ...draft, openingHours: e.target.value })} placeholder="For example, Mon–Fri 09:00–17:00" /></label>
          <div className="supplier-field supplier-photo-field"><span>Photo <em>(optional)</em></span>
            {/* eslint-disable-next-line @next/next/no-img-element -- a data URL needs no optimisation */}
            {image && <img className="supplier-photo-thumb" src={image} alt="Selected supplier photo" />}
            <div className="photo-actions"><label className="secondary-button file-button">{image ? "Replace photo" : "Choose photo"}<input type="file" accept="image/jpeg,image/png,image/webp" disabled={preparing} onChange={chooseImage} /></label>{image && <button type="button" className="text-button" disabled={preparing} onClick={() => setImage("")}>Remove photo</button>}</div>
            <small role="status">{preparing ? "Preparing photo…" : "JPEG, PNG or WebP. Large photos are resized before saving."}</small>
            {imageError && <p className="form-error" role="alert">{imageError}</p>}
          </div>
        </fieldset>
        {error && <p className="form-error" role="alert">{error}</p>}
        {saved && <p className="notice" role="status">Your changes have been saved.</p>}
        <div className="supplier-form-actions">{saved ? <button type="button" className="primary-button" onClick={onClose}>Done</button> : <><button className="primary-button" disabled={busy || preparing || !draft.name.trim() || !draft.locationId}>{busy ? "Saving…" : "Save supplier"}</button><button type="button" className="secondary-button" disabled={busy} onClick={onClose}>Cancel</button></>}</div>
      </form>
      <aside className="supplier-save-preview" aria-label="Record preview"><p className="auth-overline">{saved ? "Saved record" : "Record preview"}</p><div className="supplier-summary-card">
        {/* eslint-disable-next-line @next/next/no-img-element -- a data URL needs no optimisation */}
        {previewImage && <img className="supplier-photo" src={previewImage} alt={`Photo of ${summary.name || "the supplier"}`} />}
        <strong>{summary.name || "Supplier name"}</strong><p>{categoryLabel(summary.category)} · {locations.find(l => l.id === summary.locationId)?.name || "Choose a location"}</p>
        <dl><div><dt>Supplier ID</dt><dd>{saved?.id ?? supplier?.id ?? "Assigned on save"}</dd></div><div><dt>Created</dt><dd>{saved || supplier ? displayTime((saved ?? supplier)!.createdAt) : "Assigned on save"}</dd></div><div><dt>Status</dt><dd>{summary.active ? "Active" : "Inactive"}</dd></div><div><dt>Description</dt><dd>{summary.description || "Not provided"}</dd></div><div><dt>Opening hours</dt><dd>{summary.openingHours || "Not provided"}</dd></div><div><dt>Photo</dt><dd>{previewImage ? "Included" : "Not provided"}</dd></div></dl>
      </div><p className="supplier-preview-note">Only one active supplier can use the same name at a location. The same name at a different location is allowed.</p></aside>
    </div>
  </Modal>;
}
