export async function api<T>(path: string, token: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${token}`);
  if (!token) headers.delete("Authorization");
  if (init.body && !(init.body instanceof FormData)) headers.set("Content-Type", "application/json");

  const method = (init.method || "GET").toUpperCase();
  if (path.startsWith("/api/admin/") && ["POST", "PATCH", "DELETE"].includes(method) && path !== "/api/admin/login") {
    const password = window.prompt("Enter your current admin password to confirm this change.");
    if (password === null || !password.trim()) throw new Error("Password confirmation is required to apply this change.");
    headers.set("X-Admin-Password", password);
  }

  const response = await fetch(path, { ...init, headers });
  const text = await response.text();
  let payload: unknown;
  try {
    payload = text ? JSON.parse(text) : null;
  } catch {
    throw new Error("The server returned an invalid response.");
  }
  if (!response.ok) {
    const root = payload as { error?: { message?: string }; message?: string } | null;
    throw new Error(root?.error?.message || root?.message || `Request failed (${response.status}).`);
  }
  return payload as T;
}

export async function apiBlob(path: string, token: string): Promise<Blob> {
  const response = await fetch(path, { headers: { Authorization: `Bearer ${token}` } });
  if (!response.ok) {
    let message = `Request failed (${response.status}).`;
    try {
      const payload = await response.json() as { error?: { message?: string }; message?: string };
      message = payload.error?.message || payload.message || message;
    } catch {
      // Error responses may not include JSON.
    }
    throw new Error(message);
  }
  return response.blob();
}

export const date = (value?: string | null) => value ? new Date(value).toLocaleString() : "Never";

export const formatBytes = (value: number) => {
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let amount = value / 1024;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${amount.toFixed(amount < 10 ? 1 : 0)} ${units[unit]}`;
};
