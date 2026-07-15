// Typed client for the admin JSON API (see internal/api/admin.go).

export interface Overview {
  startedAt: number;
  uptimeSeconds: number;
  goVersion: string;
  users: { static: number; accounts: number };
  storage: { bytes: number; rows: number; dbFileBytes: number };
  requests: { total: number; clientErrors: number; serverErrors: number };
  config: {
    addr: string;
    signupAllowed: boolean;
    maxUserBytes: number;
    maxBodyBytes: number;
  };
}

export type UserSource = "static" | "account" | "orphaned";

export interface AdminUser {
  username: string;
  source: UserSource;
  createdAt?: number;
  bytes: number;
  rows: number;
  devices: number;
  lastChangeAt?: number;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    const msg =
      body && typeof body.error === "string"
        ? body.error
        : `${res.status} ${res.statusText}`;
    throw new Error(msg);
  }
  return body as T;
}

export const fetchOverview = () => request<Overview>("/api/admin/overview");

export const fetchUsers = () =>
  request<{ users: AdminUser[] }>("/api/admin/users").then((b) => b.users);

export const setPassword = (username: string, password: string) =>
  request<void>(`/api/admin/users/${encodeURIComponent(username)}/password`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  });

export const wipeData = (username: string) =>
  request<{ deletedRows: number }>(
    `/api/admin/users/${encodeURIComponent(username)}/data`,
    { method: "DELETE" },
  );

export const deleteAccount = (username: string) =>
  request<void>(`/api/admin/users/${encodeURIComponent(username)}`, {
    method: "DELETE",
  });
