"use client";

import { useEffect, useRef, useState } from "react";
import { Bell, CircleAlert, Lightbulb, ServerCrash } from "lucide-react";
import { api, date } from "@/lib/admin-api";
import type { AdminAlert, PageKey } from "@/lib/admin-types";

export function AlertsMenu({ token, onNavigate }: { token: string; onNavigate: (page: PageKey) => void }) {
  const [alerts, setAlerts] = useState<AdminAlert[]>([]);
  const [loadError, setLoadError] = useState("");
  const [newCount, setNewCount] = useState(0);
  const seen = useRef<Set<string> | null>(null);

  useEffect(() => {
    let active = true;
    const refresh = async () => {
      try {
        const next = await api<AdminAlert[]>("/api/admin/alerts", token);
        if (!active) return;
        if (seen.current) {
          const unseenCount = next.filter((item) => !seen.current?.has(item.id)).length;
          setNewCount((count) => count + unseenCount);
        } else {
          setNewCount(next.length);
        }
        seen.current = new Set(next.map((item) => item.id));
        setAlerts(next);
        setLoadError("");
      } catch (error) {
        if (active) setLoadError(error instanceof Error ? error.message : "Could not load alerts.");
      }
    };
    void refresh();
    const interval = window.setInterval(() => void refresh(), 30_000);
    return () => { active = false; window.clearInterval(interval); };
  }, [token]);

  const iconFor = (category: AdminAlert["category"]) => category === "insight" ? <Lightbulb size={16} /> : category === "server_error" ? <ServerCrash size={16} /> : <CircleAlert size={16} />;
  return <details className="alerts-menu" onToggle={(event) => { if (event.currentTarget.open) setNewCount(0); }}>
    <summary className="alerts-trigger" aria-label="Admin alerts">
      <Bell size={18} />
      {newCount > 0 && <span className="alerts-count">{newCount > 99 ? "99+" : newCount}</span>}
    </summary>
    <div className="alerts-popover">
      <div className="alerts-heading"><span><strong>Admin alerts</strong><small>New errors, insights and server failures</small></span><i className={alerts.length ? "alerts-live" : ""} /></div>
      {loadError && <p className="alerts-error">{loadError}</p>}
      <div className="alerts-list">{alerts.slice(0, 12).map((item) => <button key={`${item.category}-${item.id}`} onClick={() => onNavigate("insights")}>
        <i className={`alert-icon ${item.category}`}>{iconFor(item.category)}</i><span><strong>{item.title}</strong><small>{item.details} · {date(item.createdAt)}</small></span>
      </button>)}
      {!alerts.length && !loadError && <p className="alerts-empty">You’re all caught up.</p>}</div>
    </div>
  </details>;
}
