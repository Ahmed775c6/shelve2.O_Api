"use client";

import { useState, type FormEvent } from "react";
import { api } from "@/lib/admin-api";
import type { PublicBook } from "@/lib/admin-types";

export function LibraryPage({ books, token, reload }: { books: PublicBook[]; token: string; reload: () => Promise<void> }) {
  const [title, setTitle] = useState("");
  const [author, setAuthor] = useState("");
  const [category, setCategory] = useState("");
  const [description, setDescription] = useState("");
  const [pages, setPages] = useState("");
  const [coverColor, setCoverColor] = useState("cover-blue");
  const [bookFile, setBookFile] = useState<File | null>(null);
  const [coverFile, setCoverFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const publish = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const form = new FormData();
      form.set("book", JSON.stringify({ title, author, category, description, totalPages: Number(pages) || 0, coverColor }));
      if (bookFile) form.set("bookFile", bookFile);
      if (coverFile) form.set("coverFile", coverFile);
      await api("/api/admin/books", token, { method: "POST", body: form });
      setTitle("");
      setAuthor("");
      setCategory("");
      setDescription("");
      setPages("");
      setBookFile(null);
      setCoverFile(null);
      await reload();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not publish book.");
    } finally {
      setBusy(false);
    }
  };

  const remove = async (book: PublicBook) => {
    if (!window.confirm(`Remove "${book.title}" from Shelve Library?`)) return;
    try {
      await api(`/api/admin/books/${book.id}`, token, { method: "DELETE" });
      await reload();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "Could not remove book.");
    }
  };

  return <div className="library-layout">
    <section className="panel publish-panel"><div className="panel-heading"><div><p className="eyebrow">COMMUNITY CATALOG</p><h2>Publish a book</h2><p className="muted">Published books are visible to everyone in Shelve Library.</p></div></div>
      <form className="publish-form" onSubmit={(event) => void publish(event)}>
        <div className="form-grid"><label>Title<input required maxLength={200} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Book title" /></label><label>Author<input required maxLength={200} value={author} onChange={(event) => setAuthor(event.target.value)} placeholder="Author name" /></label><label>Category<input maxLength={100} value={category} onChange={(event) => setCategory(event.target.value)} placeholder="Fiction, history…" /></label><label>Pages<input type="number" min="0" max="100000" value={pages} onChange={(event) => setPages(event.target.value)} placeholder="Optional" /></label></div>
        <label>Description<textarea rows={4} maxLength={10000} value={description} onChange={(event) => setDescription(event.target.value)} placeholder="A short note about this book" /></label>
        <div className="form-grid"><label>Cover color<select value={coverColor} onChange={(event) => setCoverColor(event.target.value)}>{["blue", "purple", "red", "green", "teal", "amber", "slate", "rose"].map((color) => <option key={color} value={`cover-${color}`}>{color[0].toUpperCase() + color.slice(1)}</option>)}</select></label><label>Cover image <small>Optional · PNG, JPG or WebP · 5 MB max</small><input type="file" accept=".png,.jpg,.jpeg,.webp,image/png,image/jpeg,image/webp" onChange={(event) => setCoverFile(event.target.files?.[0] || null)} /></label></div>
        <label>Book file <small>Optional · PDF, ePub, MOBI or TXT · 20 MB max</small><input type="file" accept=".pdf,.epub,.mobi,.txt,application/pdf,text/plain" onChange={(event) => setBookFile(event.target.files?.[0] || null)} /></label>
        {error && <p className="notice error">{error}</p>}
        <button className="button primary" disabled={busy}>{busy ? "Publishing…" : "Publish to library"}</button>
      </form>
    </section>
    <section className="panel"><div className="panel-heading"><div><p className="eyebrow">PUBLISHED</p><h2>{books.length} books</h2></div></div>{books.map((book) => <div className="published-book" key={book.id}><div className="mini-cover">▣</div><span><strong>{book.title}</strong><small>{book.author} · {book.category || "Uncategorized"} {book.fileName ? "· file attached" : ""}</small></span><button className="icon-button remove-button" title={`Remove ${book.title}`} onClick={() => void remove(book)}>×</button></div>)}{!books.length && <div className="empty-state">Your published books will appear here.</div>}</section>
  </div>;
}
