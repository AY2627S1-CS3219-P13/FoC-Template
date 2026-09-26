export const categories = ["Food", "Printing", "Retail", "Services", "Other"] as const;
export type Category = (typeof categories)[number];

export const locations = ["University Town", "Central Library", "Computing", "Science", "Kent Ridge"] as const;
export type Location = (typeof locations)[number];

export type Supplier = {
  id: string;
  createdAt: string;
  name: string;
  category: Category;
  location: Location;
  description: string;
  openingHours: string;
  active: boolean;
};

export type SupplierDraft = Omit<Supplier, "id" | "createdAt" | "location"> & { location: Location | "" };

export const emptyDraft: SupplierDraft = {
  name: "",
  category: "Food",
  location: "",
  description: "",
  openingHours: "",
  active: true,
};

// Clearly fictional sample records. Replace this adapter once Supplier Service has an API contract.
export const sampleSuppliers: Supplier[] = [
  {
    id: "demo-1",
    createdAt: "2026-09-01T09:00:00Z",
    name: "Campus Bites",
    category: "Food",
    location: "University Town",
    description: "A sample campus café for the Supplier Service showcase.",
    openingHours: "Mon–Fri, 8:00–18:00",
    active: true,
  },
  {
    id: "demo-2",
    createdAt: "2026-09-02T09:00:00Z",
    name: "Quick Print",
    category: "Printing",
    location: "Central Library",
    description: "Sample printing and document collection point.",
    openingHours: "Mon–Sat, 9:00–20:00",
    active: true,
  },
  {
    id: "demo-3",
    createdAt: "2026-09-03T09:00:00Z",
    name: "Campus Essentials",
    category: "Retail",
    location: "Science",
    description: "Sample everyday supplies for campus errands.",
    openingHours: "Daily, 10:00–19:00",
    active: true,
  },
  {
    id: "demo-4",
    createdAt: "2026-09-04T09:00:00Z",
    name: "Parcel Point",
    category: "Services",
    location: "Computing",
    description: "A sample pickup location, currently marked inactive.",
    openingHours: "Mon–Fri, 9:00–17:00",
    active: false,
  },
];

export function duplicateActiveName(suppliers: Supplier[], name: string, location: Location, exceptId?: string) {
  return suppliers.some(
    (supplier) =>
      supplier.active &&
      supplier.id !== exceptId &&
      supplier.location === location &&
      supplier.name.toLocaleLowerCase() === name.toLocaleLowerCase(),
  );
}
