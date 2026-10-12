export type Wallet = { userId: string; available: number; reserved: number };
export type Transaction = {
  id: number;
  kind: "allocation" | "reserve" | "release" | "spend" | "receive" | "admin_debit";
  amount: number;
  availableDelta: number;
  reservedDelta: number;
  availableAfter: number;
  reservedAfter: number;
  orderId?: string;
  counterpartyId?: string;
  createdAt: string;
};
export type TransactionPage = { transactions: Transaction[]; nextBefore: number | null };

export class CreditApiError extends Error {
  constructor(public readonly status: number, message: string) { super(message); }
}

async function request<T>(path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, { credentials: "include", cache: "no-store", signal });
  const data = await response.json().catch(() => undefined);
  if (!response.ok) throw new CreditApiError(response.status, data?.error?.message ?? "Credits unavailable. Please retry.");
  return data as T;
}

export const creditApi = {
  wallet: (signal?: AbortSignal) => request<Wallet>("/api/v1/wallets/me", signal),
  transactions: (before: number | null, signal?: AbortSignal) => {
    const query = new URLSearchParams({ limit: "10" });
    if (before !== null) query.set("before", String(before));
    return request<TransactionPage>(`/api/v1/wallets/me/transactions?${query}`, signal);
  },
};
