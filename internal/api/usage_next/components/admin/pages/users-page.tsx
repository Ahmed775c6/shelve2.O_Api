"use client";

import { useMemo, useState, type FormEvent } from "react";
import { api, date } from "@/lib/admin-api";
import type { User, UserDetails } from "@/lib/admin-types";

function EmptyState({ text }: { text: string }) {
  return <div className="empty-state">{text}</div>;
}

function Health({ label, value }: { label: string; value: string | number }) {
  return <div className="health-item"><small>{label}</small><strong>{value}</strong></div>;
}

export function UsersPage({ users, token, onSaved }: { users: User[]; token: string; onSaved: () => Promise<void> }) {
  const [query, setQuery] = useState("");
  const [selectedUser, setSelectedUser] = useState<UserDetails | null>(null);
  const [busy, setBusy] = useState(false);
  const filtered = useMemo(() => users.filter((user) =>
    `${user.name} ${user.email} ${user.platform}`.toLowerCase().includes(query.toLowerCase()),
  ), [users, query]);

  const openUser = async (user: User) => {
    setBusy(true);
    try {
      setSelectedUser(await api<UserDetails>(`/api/admin/users/${user.id}`, token));
    } catch (failure) {
      window.alert(failure instanceof Error ? failure.message : "Could not load user.");
    } finally {
      setBusy(false);
    }
  };

  return <>
    <section className="panel table-panel"><div className="panel-heading"><div><p className="eyebrow">ACCOUNTS</p><h2>{users.length} members</h2></div><input className="search-input" placeholder="Search name, email or platform…" value={query} onChange={(event) => setQuery(event.target.value)} /></div>
      <div className="table-scroll"><table><thead><tr><th>User</th><th>Platform</th><th>IP address</th><th>Library</th><th>Last seen</th><th>Status</th><th /></tr></thead><tbody>
        {filtered.map((user) => <tr key={user.id} onClick={() => void openUser(user)}><td><span className="user-cell"><i className="avatar">{(user.name || user.email).slice(0, 1).toUpperCase()}</i><span><strong>{user.name || "Unnamed user"}</strong><small>{user.email}</small></span></span></td><td>{user.platform || "Unknown"}</td><td className="mono">{user.lastIpAddress || "Unavailable"}</td><td>{user.books} books · {user.writings} writings</td><td>{date(user.lastSeenAt)}</td><td><span className={`status-pill ${user.banned ? "banned" : "healthy"}`}><i />{user.banned ? "Banned" : "Active"}</span></td><td><button className="row-action" disabled={busy}>View →</button></td></tr>)}
        {!filtered.length && <tr><td colSpan={7}><EmptyState text="No matching users." /></td></tr>}
      </tbody></table></div>
    </section>
    {selectedUser && <UserModal user={selectedUser} token={token} onClose={() => setSelectedUser(null)} onSaved={async () => { setSelectedUser(null); await onSaved(); }} />}
  </>;
}

function UserModal({ user, token, onClose, onSaved }: { user: UserDetails; token: string; onClose: () => void; onSaved: () => Promise<void> }) {
  const [name, setName] = useState(user.name);
  const [email, setEmail] = useState(user.email);
  const [banned, setBanned] = useState(user.banned);
  const [newPassword, setNewPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api(`/api/admin/users/${user.id}`, token, {
        method: "PATCH",
        body: JSON.stringify({ name, email, banned, password: newPassword || undefined }),
      });
      await onSaved();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not update user.");
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!window.confirm(`Permanently delete ${user.email} and all account data?`)) return;
    setBusy(true);
    setError("");
    try {
      await api(`/api/admin/users/${user.id}`, token, { method: "DELETE" });
      await onSaved();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not delete user.");
      setBusy(false);
    }
  };

  return <div className="modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}><section className="user-modal" role="dialog" aria-modal="true" aria-labelledby="user-title">
    <header className="modal-heading"><div><p className="eyebrow">ACCOUNT CONTROL</p><h2 id="user-title">{user.name || user.email}</h2><p>{user.email}</p></div><button className="icon-button" onClick={onClose}>×</button></header>
    {error && <div className="notice error">{error}</div>}
    <form className="user-edit-form" onSubmit={(event) => void save(event)}>
      <div className="form-grid"><label>Display name<input value={name} onChange={(event) => setName(event.target.value)} required /></label><label>Email<input type="email" value={email} onChange={(event) => setEmail(event.target.value)} required /></label><label className="span-two">Set new password<input type="password" minLength={8} maxLength={72} value={newPassword} onChange={(event) => setNewPassword(event.target.value)} placeholder="Leave blank to keep current password" /></label></div>
      <label className="toggle-row"><span><strong>Ban account</strong><small>Immediately blocks existing sessions and future sign-ins.</small></span><input type="checkbox" checked={banned} onChange={(event) => setBanned(event.target.checked)} /></label>
      <div className="user-facts"><Health label="Platform" value={user.platform || "Unknown"} /><Health label="Joined" value={date(user.createdAt)} /><Health label="Last active" value={date(user.lastSeenAt)} /><Health label="Email verified" value={user.emailVerified ? "Yes" : "No"} /><Health label="Books" value={user.books} /><Health label="Writings" value={user.writings} /></div>
      <div className="user-ip-card"><span>Most recent IP address</span><strong className="mono">{user.lastIpAddress || "Unavailable"}</strong></div>
      <footer className="modal-actions"><button type="button" className="button danger-outline" onClick={() => void remove()} disabled={busy}>Delete account</button><span /><button type="button" className="button subtle" onClick={onClose}>Cancel</button><button className="button primary" disabled={busy}>{busy ? "Saving…" : "Save changes"}</button></footer>
    </form>
    <section className="activity-section"><div className="panel-heading"><div><p className="eyebrow">RECENT ACTIVITY</p><h3>Latest sessions and API actions</h3></div></div>
      {(user.activities || []).slice(0, 12).map((activity, index) => <div className="activity-row" key={`${activity.createdAt}-${index}`}><span className={`activity-status ${activity.status < 400 ? "" : "bad"}`}>{activity.status}</span><span><strong>{activity.method} {activity.path}</strong><small>{activity.ipAddress || activity.platform || "IP unavailable"}</small></span><time>{date(activity.createdAt)}</time></div>)}
      {!user.activities?.length && <p className="muted">No recent activity has been captured.</p>}
    </section>
    <details className="records-details"><summary>Account records ({user.records?.length || 0})</summary>{(user.records || []).map((record) => <pre key={record.id}>{JSON.stringify({ collection: record.collection, updatedAt: record.updatedAt, data: record.data }, null, 2)}</pre>)}</details>
  </section></div>;
}
