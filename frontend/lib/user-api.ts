// Relative URLs share the gateway origin and its browser-managed session cookie.
const userApiBase = process.env.NEXT_PUBLIC_USER_API_URL ?? "";

export type User = {
  id: string;
  email: string;
  displayName: string;
  roles: string[];
  verifiedAt: string;
};

export type RoleEvent = {
  id: string;
  actorKind: "bootstrap" | "admin" | "operator";
  actorUserId: string | null;
  actorLabel: string;
  targetUserId: string;
  targetEmail: string;
  previousRole: "user" | "admin";
  newRole: "user" | "admin";
  reason: string;
  occurredAt: string;
};

export class UserApiError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly code?: string,
    public readonly registrationToken?: string,
  ) {
    super(message);
  }
}

async function request<T>(path: string, body?: object, method?: string): Promise<T> {
  const response = await fetch(`${userApiBase}${path}`, {
    method: method ?? (body === undefined ? "GET" : "POST"),
    credentials: "include",
    cache: "no-store",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = response.status === 204 ? null : await response.json();
  if (!response.ok) {
    throw new UserApiError(
      data?.error?.message ?? "The request could not be completed.",
      response.status,
      data?.error?.code,
      data?.registrationToken,
    );
  }
  return data as T;
}

export const userApi = {
  me: () => request<{ user: User }>("/api/v1/users/me"),
  findUserByEmail: (email: string) => request<{ user: User }>(`/api/v1/admin/users?${new URLSearchParams({ email })}`),
  changeUserRole: (id: string, role: "user" | "admin", currentPassword: string) =>
    request<{ userId: string; roles: string[] }>(`/api/v1/admin/users/${encodeURIComponent(id)}/role`, { role, currentPassword }, "PUT"),
  roleEvents: () => request<{ events: RoleEvent[]; total: number }>("/api/v1/admin/role-events?pageSize=20"),
  claimFirstAdmin: () => request<{ message: string }>("/api/v1/admin/bootstrap-claim", undefined, "POST"),
  login: (email: string, password: string) =>
    request<{ user: User; expiresAt: string }>("/api/v1/auth/login", { email, password }),
  // Logout has no request body, but it must be a POST.
  logout: async () => {
    const response = await fetch(`${userApiBase}/api/v1/auth/logout`, {
      method: "POST",
      credentials: "include",
      cache: "no-store",
    });
    if (!response.ok) throw new UserApiError("Could not log out. Please retry.", response.status);
  },
  register: (email: string, displayName: string, password: string) =>
    request<{ message: string; registrationToken: string }>("/api/v1/auth/register", {
      email,
      displayName,
      password,
    }),
  verify: (email: string, code: string, registrationToken: string, displayName?: string) =>
    request<{ message: string }>("/api/v1/auth/verify-email", {
      email,
      code,
      registrationToken,
      ...(displayName ? { displayName } : {}),
    }),
  resend: (email: string) =>
    request<{ message: string }>("/api/v1/auth/resend-verification", { email }),
};
