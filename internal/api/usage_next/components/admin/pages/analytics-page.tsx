"use client";

import { Activity, AlertTriangle, Clock3, Server } from "lucide-react";
import { TrendChart } from "@/components/admin/analytics-charts";
import { formatBytes } from "@/lib/admin-api";
import type { UsageAnalytics } from "@/lib/admin-types";

function Metric({ label, value, note, icon: Icon, change }: {
  label: string;
  value: string;
  note: string;
  icon: typeof Activity;
  change?: number;
}) {
  return <article className="metric-card">
    <div className="metric-top"><span>{label}</span><i><Icon size={17} /></i></div>
    <strong>{value}</strong><small>{note}</small>
    {change !== undefined && <span className={`analytics-change ${change > 0 ? "up" : change < 0 ? "down" : ""}`}>{change > 0 ? "+" : ""}{change.toFixed(1)}% vs previous</span>}
  </article>;
}

function uptimeLabel(value: number) {
  const years = Math.floor(value / 31536000);
  const months = Math.floor(value % 31536000 / 2592000);
  const days = Math.floor(value % 2592000 / 86400);
  return `${years ? `${years}y ` : ""}${months ? `${months}mo ` : ""}${days}d`;
}

export function AnalyticsPage({ analytics, period, onPeriodChange }: {
  analytics: UsageAnalytics | null;
  period: UsageAnalytics["period"];
  onPeriodChange: (period: UsageAnalytics["period"]) => void;
}) {
  if (!analytics) return <div className="empty-state">Loading usage history…</div>;
  const { comparison, series, app } = analytics;
  const current = comparison.current;
  const previous = comparison.previous;
  const requestPoints = series.map((item) => ({ date: item.period, count: item.requests }));
  const accountPoints = series.map((item) => ({ date: item.period, count: item.newUsers }));
  const errorPoints = series.map((item) => ({ date: item.period, count: item.errors }));
  const latencyPoints = series.map((item) => ({ date: item.period, count: item.avgDurationMs }));
  const peakMemory = Math.max(0, ...series.map((item) => item.peakMemoryBytes));
  const peakGoroutines = Math.max(0, ...series.map((item) => item.peakGoroutines));
  const peakConnections = Math.max(0, ...series.map((item) => item.peakDatabaseConnections));
  const peakDatabaseSize = Math.max(0, ...series.map((item) => item.peakDatabaseSizeBytes));
  const peakUptime = Math.max(0, ...series.map((item) => item.peakUptimeSeconds));
  return <div className="page-grid usage-analytics-page">
    <section className="panel usage-period-panel">
      <div><p className="eyebrow">LONG-RANGE USAGE</p><h2>Application and server analytics</h2><p className="muted">Historical traffic is retained as daily aggregates, independent of the 30-day request-detail log.</p></div>
      <div className="request-tabs" role="tablist" aria-label="Analytics period">
        {([["day", "Daily"], ["month", "Monthly"], ["year", "Yearly"]] as const).map(([key, label]) => <button key={key} type="button" role="tab" aria-selected={period === key} className={period === key ? "active" : ""} onClick={() => onPeriodChange(key)}>{label}</button>)}
      </div>
    </section>
    <section className="metric-grid usage-summary-grid">
      <Metric label="Requests" value={current.requests.toLocaleString()} note={`${current.label} · prev ${previous.requests.toLocaleString()}`} icon={Activity} change={comparison.requestChangePercent} />
      <Metric label="New accounts" value={current.newUsers.toLocaleString()} note={`${current.label} · prev ${previous.newUsers.toLocaleString()}`} icon={Activity} />
      <Metric label="Errors" value={current.errors.toLocaleString()} note={`${current.label} · prev ${previous.errors.toLocaleString()}`} icon={AlertTriangle} change={comparison.errorChangePercent} />
      <Metric label="Average latency" value={`${current.avgDurationMs.toLocaleString()} ms`} note={`Peak ${current.maxDurationMs.toLocaleString()} ms`} icon={Clock3} />
      <Metric label="API server uptime" value={uptimeLabel(app.uptimeSeconds)} note={`${app.platform} · ${app.goVersion}`} icon={Server} />
    </section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">{current.label.toUpperCase()}</p><h2>Requests by period</h2></div><span className="chart-legend"><i /> Requests</span></div><TrendChart values={requestPoints} kind="area" color="#bba4f4" /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">{current.label.toUpperCase()}</p><h2>New accounts by period</h2></div><span className="chart-legend purple"><i /> Accounts</span></div><TrendChart values={accountPoints} kind="line" color="#70d7b6" unit="new accounts" /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">{current.label.toUpperCase()}</p><h2>Errors by period</h2></div><span className="chart-legend red"><i /> 4xx + 5xx</span></div><TrendChart values={errorPoints} kind="bar" color="#f18f91" unit="errors" /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">{current.label.toUpperCase()}</p><h2>Average response time</h2></div><span className="chart-legend purple"><i /> Milliseconds</span></div><TrendChart values={latencyPoints} kind="line" color="#83a8ff" unit="milliseconds" /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">OBSERVED SERVER PEAKS</p><h2>Runtime resource use</h2></div></div>
      <div className="health-grid"><div className="health-item"><small>Memory in use</small><strong>{formatBytes(peakMemory)}</strong></div><div className="health-item"><small>Goroutines</small><strong>{peakGoroutines.toLocaleString()}</strong></div><div className="health-item"><small>Database connections</small><strong>{peakConnections.toLocaleString()}</strong></div><div className="health-item"><small>Database size</small><strong>{formatBytes(peakDatabaseSize)}</strong></div><div className="health-item"><small>Observed process uptime</small><strong>{uptimeLabel(peakUptime)}</strong></div></div>
    </section>
    <section className="panel usage-period-comparison">
      <div className="panel-heading"><div><p className="eyebrow">PERIOD COMPARISON</p><h2>{current.label} vs {previous.label}</h2></div></div>
      <div className="comparison-table"><div><span>Metric</span><strong>Current</strong><strong>Previous</strong></div>
        <div><span>Requests</span><strong>{current.requests.toLocaleString()}</strong><strong>{previous.requests.toLocaleString()}</strong></div>
        <div><span>New accounts</span><strong>{current.newUsers.toLocaleString()}</strong><strong>{previous.newUsers.toLocaleString()}</strong></div>
        <div><span>Successful responses</span><strong>{current.success.toLocaleString()}</strong><strong>{previous.success.toLocaleString()}</strong></div>
        <div><span>Client errors (4xx)</span><strong>{current.clientErrors.toLocaleString()}</strong><strong>{previous.clientErrors.toLocaleString()}</strong></div>
        <div><span>Server errors (5xx)</span><strong>{current.serverErrors.toLocaleString()}</strong><strong>{previous.serverErrors.toLocaleString()}</strong></div>
        <div><span>Average response time</span><strong>{current.avgDurationMs.toLocaleString()} ms</strong><strong>{previous.avgDurationMs.toLocaleString()} ms</strong></div>
      </div>
      <p className="request-privacy-note">Server memory, goroutine, connection, and uptime peaks are sampled on API traffic and admin health checks. Historical charts begin when aggregate collection was introduced.</p>
    </section>
  </div>;
}
