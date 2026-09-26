export const categories = ["food", "printing", "retail", "services", "other"] as const;
export type Category = (typeof categories)[number];
export type CampusLocation = { id: string; name: string };
export type Supplier = {
  id: string;
  name: string;
  category: Category;
  locationId: string;
  description: string;
  openingHours: string;
  active: boolean;
  createdAt: string;
  updatedAt: string;
};
export type SupplierDraft = Omit<Supplier, "id" | "createdAt" | "updatedAt">;
export type CatalogueQuery = {
  q: string; category: string; locationId: string;
  status: "all" | "active" | "inactive";
  sort: "name" | "category" | "location" | "createdAt";
  direction: "asc" | "desc";
  page: number; pageSize: number;
};
export type SupplierPage = { suppliers: Supplier[]; page: number; pageSize: number; total: number; totalPages: number };
export const initialQuery: CatalogueQuery = { q: "", category: "", locationId: "", status: "all", sort: "name", direction: "asc", page: 1, pageSize: 10 };
export const emptyDraft: SupplierDraft = { name: "", category: "food", locationId: "", description: "", openingHours: "", active: true };
export const categoryLabel = (value: string) => value.charAt(0).toUpperCase() + value.slice(1);
export const displayTime = (value: string) => new Date(value).toLocaleString();
