"use client";

import Image from "next/image";
import { useEffect, useRef, useState } from "react";
import { Bug, Eye, Image as ImageIcon, Lightbulb, X } from "lucide-react";
import { api, apiBlob, date, formatBytes } from "@/lib/admin-api";
import type { ErrorAttachment, ErrorItem, Insight } from "@/lib/admin-types";

type FeedbackTab = "bugs" | "insights";
type ScreenshotPreview = { url: string; filename: string };

function ScreenshotThumbnail({ report, attachment, token, onOpen }: {
  report: ErrorItem;
  attachment: ErrorAttachment;
  token: string;
  onOpen: (preview: ScreenshotPreview) => void;
}) {
  const buttonRef = useRef<HTMLButtonElement>(null);
  const objectUrl = useRef("");
  const [url, setUrl] = useState("");
  const [blob, setBlob] = useState<Blob | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    let active = true;
    let started = false;
    const load = async () => {
      if (started) return;
      started = true;
      setLoading(true);
      try {
        const blob = await apiBlob(`/api/admin/errors/${report.id}/attachments/${attachment.id}`, token);
        if (!blob.type.startsWith("image/")) throw new Error("The server did not return an image for this screenshot.");
        const createdUrl = URL.createObjectURL(blob);
        if (!active) {
          URL.revokeObjectURL(createdUrl);
          return;
        }
        objectUrl.current = createdUrl;
        setBlob(blob);
        setUrl(createdUrl);
      } catch (failure) {
        if (active) setError(failure instanceof Error ? failure.message : "Could not load screenshot.");
      } finally {
        if (active) setLoading(false);
      }
    };
    const element = buttonRef.current;
    if (!element || typeof IntersectionObserver === "undefined") {
      void load();
    } else {
      const observer = new IntersectionObserver((entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          observer.disconnect();
          void load();
        }
      }, { rootMargin: "120px" });
      observer.observe(element);
      return () => {
        active = false;
        observer.disconnect();
        if (objectUrl.current) URL.revokeObjectURL(objectUrl.current);
      };
    }
    return () => {
      active = false;
      if (objectUrl.current) URL.revokeObjectURL(objectUrl.current);
    };
  }, [attachment.id, report.id, token]);

  return <button ref={buttonRef} type="button" className="feedback-screenshot-thumb" disabled={!url || !blob} onClick={() => {
    if (blob) onOpen({ url: URL.createObjectURL(blob), filename: attachment.filename });
  }} title={`${attachment.filename} · ${formatBytes(attachment.sizeBytes)}`}>
    {url
      ? <Image src={url} alt={`${report.title || "Bug report"} screenshot`} width={320} height={180} unoptimized />
      : <span>{error || (loading ? "Loading screenshot…" : attachment.filename)}</span>}
    <small><Eye size={12} />{attachment.filename}</small>
  </button>;
}

export function FeedbackInbox({ errors, insights, token, reload }: {
  errors: ErrorItem[];
  insights: Insight[];
  token: string;
  reload: () => Promise<void>;
}) {
  const [tab, setTab] = useState<FeedbackTab>("bugs");
  const [busyId, setBusyId] = useState("");
  const [notice, setNotice] = useState("");
  const [preview, setPreview] = useState<ScreenshotPreview | null>(null);
  const openCount = errors.filter((item) => !item.resolvedAt).length;
  const newInsights = insights.filter((item) => item.status === "new").length;

  useEffect(() => {
    if (!preview) return;
    return () => URL.revokeObjectURL(preview.url);
  }, [preview]);

  const toggleError = async (item: ErrorItem) => {
    setBusyId(item.id);
    setNotice("");
    try {
      await api(`/api/admin/errors/${item.id}`, token, {
        method: "PATCH",
        body: JSON.stringify({ resolved: !item.resolvedAt }),
      });
      await reload();
    } catch (failure) {
      setNotice(failure instanceof Error ? failure.message : "Could not update this bug report.");
    } finally {
      setBusyId("");
    }
  };

  const updateInsight = async (item: Insight, status: string) => {
    setBusyId(item.id);
    setNotice("");
    try {
      await api(`/api/admin/reports/${item.id}`, token, { method: "PATCH", body: JSON.stringify({ status }) });
      await reload();
    } catch (failure) {
      setNotice(failure instanceof Error ? failure.message : "Could not update this insight.");
    } finally {
      setBusyId("");
    }
  };

  return <div className="feedback-inbox">
    <section className="panel feedback-inbox-panel">
      <div className="feedback-inbox-tabs" role="tablist" aria-label="User feedback">
        <button type="button" role="tab" aria-selected={tab === "bugs"} className={tab === "bugs" ? "active" : ""} onClick={() => setTab("bugs")}><Bug size={15} />Bug reports<span>{openCount}</span></button>
        <button type="button" role="tab" aria-selected={tab === "insights"} className={tab === "insights" ? "active" : ""} onClick={() => setTab("insights")}><Lightbulb size={15} />Insights<span>{newInsights}</span></button>
      </div>
      {notice && <div className="notice error" role="alert">{notice}<button type="button" onClick={() => setNotice("")}>×</button></div>}
      {tab === "bugs" ? <section className="card-list-panel">
        <div className="panel-heading"><div><p className="eyebrow">USER-SUBMITTED BUGS</p><h2>{openCount} open reports</h2></div><span className="muted">{errors.length} total</span></div>
        {errors.map((item) => <article className="report-card" key={item.id}>
          <div className="report-title"><span className={`status-pill ${item.resolvedAt ? "healthy" : "banned"}`}>{item.resolvedAt ? "Resolved" : "Open"}</span><strong>{item.title || item.screen || "App error"}</strong><small>{item.platform} · {date(item.createdAt)}</small></div>
          <div className="error-report-facts"><span><small>Reporter</small><strong>{item.userName} · {item.userEmail}</strong></span><span><small>Current screen</small><strong>{item.screen || "Not provided"}</strong></span></div>
          <p>{item.message}</p>
          {item.details && <div className="report-detail-block"><strong>Additional details</strong><pre>{item.details}</pre></div>}
          {item.steps && <div className="report-detail-block"><strong>Steps to reproduce</strong><pre>{item.steps}</pre></div>}
          {!!Object.keys(item.metadata || {}).length && <details className="report-metadata"><summary>Diagnostic context</summary><dl>{Object.entries(item.metadata).map(([key, value]) => <div key={key}><dt>{key}</dt><dd>{typeof value === "string" ? value : JSON.stringify(value)}</dd></div>)}</dl></details>}
          {!!item.attachments?.length && <div className="report-attachments"><strong><ImageIcon size={14} />Screenshots ({item.attachments.length})</strong><div className="feedback-screenshot-grid">{item.attachments.map((attachment) => <ScreenshotThumbnail key={attachment.id} report={item} attachment={attachment} token={token} onOpen={setPreview} />)}</div></div>}
          <div className="report-footer"><span>Report ID <code>{item.id}</code></span><button type="button" className={`button ${item.resolvedAt ? "subtle" : "primary"}`} disabled={busyId === item.id} onClick={() => void toggleError(item)}>{item.resolvedAt ? "Reopen" : "Mark resolved"}</button></div>
        </article>)}
        {!errors.length && <div className="empty-state">User bug reports will appear here.</div>}
      </section> : <section className="card-list-panel">
        <div className="panel-heading"><div><p className="eyebrow">PRODUCT FEEDBACK</p><h2>Ideas from the community</h2></div><span className="status-pill">{newInsights} new</span></div>
        {insights.map((item) => <article className="report-card" key={item.id}>
          <div className="report-title"><span className="category-tag">{item.category}</span><span className="status-pill">{item.status.replace("_", " ")}</span><small>{date(item.createdAt)}</small></div>
          <p className="insight-message">{item.message}</p>
          <div className="report-footer"><span>From <strong>{item.userName}</strong> · {item.userEmail}</span><select value={item.status} disabled={busyId === item.id} onChange={(event) => void updateInsight(item, event.target.value)} aria-label="Insight status"><option value="new">New</option><option value="reviewing">Reviewing</option><option value="resolved">Resolved</option></select></div>
        </article>)}
        {!insights.length && <div className="empty-state">User suggestions will appear here.</div>}
      </section>}
    </section>
    {preview && <ScreenshotPreviewModal preview={preview} onClose={() => setPreview(null)} />}
  </div>;
}

function ScreenshotPreviewModal({ preview, onClose }: { preview: ScreenshotPreview; onClose: () => void }) {
  return <div className="modal-backdrop screenshot-modal-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget) onClose(); }}>
    <section className="screenshot-modal" role="dialog" aria-modal="true" aria-label="Error report screenshot">
      <header><strong>{preview.filename}</strong><button type="button" className="icon-button" aria-label="Close screenshot" onClick={onClose}><X size={17} /></button></header>
      <Image src={preview.url} alt={preview.filename} width={1600} height={1200} unoptimized />
    </section>
  </div>;
}
