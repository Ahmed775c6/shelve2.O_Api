"use client";

import { useMemo, useState } from "react";
import { Eye, X } from "lucide-react";
import type { RequestItem } from "@/lib/admin-types";
import { date, formatBytes } from "@/lib/admin-api";

type RequestFilter = "all" | "admin" | "user";

export function RequestsPage({ items }: { items: RequestItem[] }) {
  const [filter, setFilter] = useState<RequestFilter>("all");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<RequestItem | null>(null);
  const filtered = useMemo(() => items.filter((item) => {
    const matchesType = filter === "all" || item.actorType === filter;
    const matchesQuery = `${item.method} ${item.path} ${item.ipAddress} ${item.actorId} ${item.userAgent}`.toLowerCase().includes(query.toLowerCase());
    return matchesType && matchesQuery;
  }), [filter, items, query]);
  return <section className="panel table-panel">
    <div className="panel-heading request-heading"><div><p className="eyebrow">API TELEMETRY</p><h2>Request activity</h2><p className="muted">Admin and user traffic are attributed separately. IPs reflect the connected peer.</p></div>
      <input className="search-input" placeholder="Search path, IP, actor or platform…" value={query} onChange={(event) => setQuery(event.target.value)} />
    </div>
    <div className="request-tabs" role="tablist" aria-label="Request type">
      {(["all", "admin", "user"] as RequestFilter[]).map((type) => <button key={type} type="button" className={filter === type ? "active" : ""} onClick={() => setFilter(type)}>{type === "all" ? "All requests" : `${type[0].toUpperCase()}${type.slice(1)} requests`} <span>{type === "all" ? items.length : items.filter((item) => item.actorType === type).length}</span></button>)}
    </div>
    <div className="table-scroll"><table><thead><tr><th>Actor</th><th>IP address</th><th>Method</th><th>Path</th><th>Status</th><th>Duration</th><th>Time</th><th /></tr></thead>
      <tbody>{filtered.map((item) => <tr key={item.id}>
        <td><span className="actor-cell"><span className={`actor-tag ${item.actorType}`}>{item.actorType}</span><small className="mono">{item.actorId || "—"}</small></span></td>
        <td className="mono">{item.ipAddress || "Unavailable"}</td>
        <td><span className="method-tag">{item.method}</span></td>
        <td className="mono request-path">{item.path}</td>
        <td><span className={`activity-status ${item.status < 400 ? "" : "bad"}`}>{item.status}</span></td>
        <td>{item.durationMs} ms</td><td>{date(item.createdAt)}</td>
        <td><button type="button" className="row-action" onClick={() => setSelected(item)}><Eye size={14} /> View</button></td>
      </tr>)}
      {!filtered.length && <tr><td colSpan={8}><div className="empty-state">No matching requests.</div></td></tr>}
      </tbody>
    </table></div>
    {selected && <div className="modal-backdrop request-detail-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) setSelected(null); }}>
      <section className="request-detail-modal" role="dialog" aria-modal="true" aria-labelledby="request-detail-title">
        <header className="modal-heading"><div><p className="eyebrow">REQUEST #{selected.id}</p><h2 id="request-detail-title">{selected.method} {selected.path}</h2></div><button type="button" className="icon-button" aria-label="Close" onClick={() => setSelected(null)}><X size={17} /></button></header>
        <div className="request-detail-grid">
          <div><small>Actor</small><strong>{selected.actorType} · {selected.actorId || "Anonymous"}</strong></div>
          <div><small>Peer IP address</small><strong className="mono">{selected.ipAddress || "Unavailable"}</strong></div>
          <div><small>HTTP status</small><strong className={selected.status >= 400 ? "text-danger" : ""}>{selected.status}</strong></div>
          <div><small>Duration</small><strong>{selected.durationMs.toLocaleString()} ms</strong></div>
          <div><small>Request size</small><strong>{formatBytes(selected.requestBytes)}</strong></div>
          <div><small>Response size</small><strong>{formatBytes(selected.responseBytes)}</strong></div>
          <div><small>Time</small><strong>{date(selected.createdAt)}</strong></div>
          <div className="request-detail-wide"><small>Client platform / user agent</small><strong>{selected.userAgent || "Unavailable"}</strong></div>
        </div>
        <p className="request-privacy-note">Request bodies, query-string values, cookies, and authorization headers are intentionally not retained.</p>
      </section>
    </div>}
  </section>;
}
