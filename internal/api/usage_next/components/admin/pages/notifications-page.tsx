"use client";

import { useState, type FormEvent } from "react";
import { api } from "@/lib/admin-api";
import type { User } from "@/lib/admin-types";

export function NotificationsPage({ users, token }: { users: User[]; token: string }) {
  const [allUsers, setAllUsers] = useState(true);
  const [selected, setSelected] = useState<string[]>([]);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [channel, setChannel] = useState("both");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  const send = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    try {
      const result = await api<{ recipients: number; emailSent: number; emailFailed: number; emailSkipped: number }>("/api/admin/notifications", token, {
        method: "POST",
        body: JSON.stringify({ allUsers, userIds: allUsers ? [] : selected, title, body, channel }),
      });
      setMessage(`Sent to ${result.recipients} users. Email: ${result.emailSent} sent, ${result.emailFailed} failed, ${result.emailSkipped} skipped.`);
      setTitle("");
      setBody("");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Could not send notification.");
    } finally {
      setBusy(false);
    }
  };

  return <section className="panel publish-panel"><div className="panel-heading"><div><p className="eyebrow">REACH YOUR COMMUNITY</p><h2>Create a notification</h2><p className="muted">Deliver updates in-app, by email, or both.</p></div></div>
    <form className="publish-form" onSubmit={(event) => void send(event)}><label>Title<input required maxLength={160} value={title} onChange={(event) => setTitle(event.target.value)} /></label><label>Message<textarea rows={5} required maxLength={4000} value={body} onChange={(event) => setBody(event.target.value)} /></label>
      <label>Delivery<select value={channel} onChange={(event) => setChannel(event.target.value)}><option value="both">In-app and email</option><option value="in_app">In-app only</option><option value="email">Email only</option></select></label>
      <label className="toggle-row"><span><strong>Send to everyone</strong><small>Otherwise choose individual recipients.</small></span><input type="checkbox" checked={allUsers} onChange={(event) => setAllUsers(event.target.checked)} /></label>
      {!allUsers && <select multiple value={selected} onChange={(event) => setSelected(Array.from(event.target.selectedOptions, (option) => option.value))}>{users.map((user) => <option key={user.id} value={user.id}>{user.name} · {user.email}</option>)}</select>}
      {message && <p className="notice success">{message}</p>}<button className="button primary" disabled={busy}>{busy ? "Sending…" : "Send notification"}</button>
    </form>
  </section>;
}
