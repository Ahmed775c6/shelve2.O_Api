"use client";

import { useState, type FormEvent } from "react";
import { FileText, Plus, Save, Trash2, X } from "lucide-react";
import { api, date } from "@/lib/admin-api";
import type { AdminNote } from "@/lib/admin-types";

export function AdminNotesPage({ notes, token, onSaved }: {
  notes: AdminNote[];
  token: string;
  onSaved: () => Promise<void>;
}) {
  const [selectedId, setSelectedId] = useState("");
  const selected = notes.find((note) => note.id === selectedId);
  const [title, setTitle] = useState("");
  const [content, setContent] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const selectNote = (note: AdminNote) => {
    setSelectedId(note.id);
    setTitle(note.title);
    setContent(note.content);
    setError("");
  };

  const startNew = () => {
    setSelectedId("");
    setTitle("");
    setContent("");
    setError("");
  };

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api<AdminNote>(selected
        ? `/api/admin/notes/${selected.id}`
        : "/api/admin/notes", token, {
        method: selected ? "PATCH" : "POST",
        body: JSON.stringify({ title, content }),
      });
      await onSaved();
      startNew();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not save this note.");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (note: AdminNote) => {
    if (!window.confirm(`Delete "${note.title}"?`)) return;
    setBusy(true);
    setError("");
    try {
      await api(`/api/admin/notes/${note.id}`, token, { method: "DELETE" });
      await onSaved();
      if (selectedId === note.id) startNew();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not delete this note.");
    } finally {
      setBusy(false);
    }
  };

  return <div className="admin-notes-layout">
    <section className="panel admin-notes-list">
      <div className="panel-heading">
        <div><p className="eyebrow">PRIVATE WORKSPACE</p><h2>Your notes</h2></div>
        <button type="button" className="button primary" onClick={startNew}><Plus size={14} />New note</button>
      </div>
      {notes.map((note) => <article className={`admin-note-item ${note.id === selectedId ? "selected" : ""}`} key={note.id}>
        <button type="button" className="admin-note-select" onClick={() => selectNote(note)}>
          <FileText size={15} />
          <span><strong>{note.title}</strong><small>Updated {date(note.updatedAt)}</small></span>
        </button>
        <button type="button" className="icon-button" aria-label={`Delete ${note.title}`} disabled={busy} onClick={() => void remove(note)}><Trash2 size={14} /></button>
      </article>)}
      {!notes.length && <div className="empty-state">No private notes yet. Save a reminder or an idea here.</div>}
    </section>
    <section className="panel admin-note-editor">
      <div className="panel-heading"><div><p className="eyebrow">{selected ? "EDIT NOTE" : "NEW NOTE"}</p><h2>{selected ? "Update your note" : "Capture a thought"}</h2></div>{selected && <button className="icon-button" type="button" aria-label="New note" onClick={startNew}><X size={15} /></button>}</div>
      {error && <div className="notice error" role="alert">{error}</div>}
      <form className="admin-note-form" onSubmit={(event) => void save(event)}>
        <label>Title<input value={title} onChange={(event) => setTitle(event.target.value)} maxLength={120} required placeholder="A short reminder" /></label>
        <label>Note<textarea value={content} onChange={(event) => setContent(event.target.value)} maxLength={20000} rows={13} required placeholder="Write a private note…" /></label>
        <div className="admin-note-footer"><small>{content.length.toLocaleString()} / 20,000 characters · saved privately for your admin account</small><button className="button primary" disabled={busy || !title.trim() || !content.trim()}><Save size={14} />{busy ? "Saving…" : selected ? "Update note" : "Save note"}</button></div>
      </form>
    </section>
  </div>;
}
