import type { CampusLocation, CatalogueQuery, Supplier, SupplierDraft, SupplierPage } from "./suppliers";

export class SupplierApiError extends Error {
  constructor(public readonly status: number, message: string, public readonly code?: string) { super(message); }
}

async function request<T>(path: string, method = "GET", body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method, credentials: "include", cache: "no-store", signal,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = response.status === 204 ? undefined : await response.json().catch(() => undefined);
  if (!response.ok) throw new SupplierApiError(response.status, data?.error?.message ?? "The service could not complete this request. Please retry.", data?.error?.code);
  return data as T;
}

export function queryString(query: CatalogueQuery) {
  return new URLSearchParams(Object.entries(query).map(([key, value]) => [key, String(value)])).toString();
}
export const supplierApi = {
  list: (query: CatalogueQuery, signal?: AbortSignal) => request<SupplierPage>(`/api/v1/suppliers?${queryString(query)}`, "GET", undefined, signal),
  locations: (signal?: AbortSignal) => request<{ locations: CampusLocation[] }>("/api/v1/locations", "GET", undefined, signal),
  get: (id: string, signal?: AbortSignal) => request<{ supplier: Supplier }>(`/api/v1/suppliers/${encodeURIComponent(id)}`, "GET", undefined, signal),
  create: (draft: SupplierDraft) => request<{ supplier: Supplier }>("/api/v1/suppliers", "POST", draft),
  update: (id: string, draft: Partial<SupplierDraft>) => request<{ supplier: Supplier }>(`/api/v1/suppliers/${encodeURIComponent(id)}`, "PATCH", draft),
  remove: (id: string) => request<void>(`/api/v1/suppliers/${encodeURIComponent(id)}`, "DELETE"),
};
export const supplierError = (error: unknown) => error instanceof SupplierApiError ? error.message : "Could not reach the catalogue. Check your connection and retry.";
