import { Activity, AlertTriangle, BookOpen, UsersRound } from "lucide-react";
import { AgeBubbleChart, BreakdownChart, PieChart, RadarChart, TrendChart } from "@/components/admin/analytics-charts";
import { date, formatBytes } from "@/lib/admin-api";
import type { Dashboard } from "@/lib/admin-types";

function Metric({ label, value, note, icon: Icon, danger = false }: {
  label: string;
  value: string | number;
  note: string;
  icon: typeof UsersRound;
  danger?: boolean;
}) {
  return <article className={`metric-card ${danger ? "metric-alert" : ""}`}>
    <div className="metric-top"><span>{label}</span><i><Icon size={17} /></i></div>
    <strong>{typeof value === "number" ? value.toLocaleString() : value}</strong>
    <small>{note}</small>
  </article>;
}

function Health({ label, value }: { label: string; value: string | number }) {
  return <div className="health-item"><small>{label}</small><strong>{value}</strong></div>;
}

export function OverviewPage({ dashboard }: { dashboard: Dashboard | null }) {
  if (!dashboard) return <div className="empty-state">Loading dashboard…</div>;
  const { counts, health, charts } = dashboard;
  const uptimeYears = Math.floor(health.uptimeSeconds / 31536000);
  const uptimeMonths = Math.floor((health.uptimeSeconds % 31536000) / 2592000);
  const uptimeDays = Math.floor((health.uptimeSeconds % 2592000) / 86400);
  const uptime = `${uptimeYears ? `${uptimeYears}y ` : ""}${uptimeMonths ? `${uptimeMonths}mo ` : ""}${uptimeDays}d ${Math.floor((health.uptimeSeconds % 86400) / 3600)}h`;
  return <div className="page-grid">
    <section className="metric-grid">
      <Metric label="Total users" value={counts.users} note="Registered accounts" icon={UsersRound} />
      <Metric label="Cloud books" value={counts.books} note="Across all libraries" icon={BookOpen} />
      <Metric label="Requests · 24h" value={counts.requests24h} note="API traffic" icon={Activity} />
      <Metric label="Open errors" value={counts.unresolvedErrors} note="Needs attention" icon={AlertTriangle} danger={counts.unresolvedErrors > 0} />
    </section>
    <section className="panel health-panel">
      <div className="panel-heading"><div><p className="eyebrow">INFRASTRUCTURE</p><h2>System health</h2></div><span className="status-pill healthy"><i />{health.status}</span></div>
      <div className="health-grid">
        <Health label="Database" value={health.database} />
        <Health label="Database size" value={formatBytes(health.databaseSizeBytes)} />
        <Health label="Uptime" value={uptime} />
        <Health label="Connections" value={`${health.databaseConnections} active · ${health.databaseIdleConnections} idle`} />
        <Health label="Goroutines" value={health.goroutines} />
        <Health label="Memory in use" value={formatBytes(health.memoryBytes)} />
        <Health label="Public books" value={counts.publicBooks} />
        <Health label="Server platform" value={health.platform} />
        <Health label="Go runtime" value={health.goVersion} />
      </div>
      <small className="muted">Last checked {date(health.checkedAt)}</small>
    </section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">LAST 7 DAYS</p><h2>Request activity</h2></div><span className="chart-legend"><i /> Requests</span></div><TrendChart values={charts.requestsByDay} kind="area" color="#bba4f4" /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">LAST 7 DAYS</p><h2>New accounts</h2></div><span className="chart-legend purple"><i /> Users</span></div><TrendChart values={charts.usersByDay} kind="line" color="#70d7b6" /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">LAST 7 DAYS</p><h2>Daily request volume</h2></div><span className="chart-legend"><i /> Requests</span></div><TrendChart values={charts.requestsByDay} kind="bar" color="#f0d16a" /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">LAST 30 DAYS</p><h2>Traffic by audience</h2></div></div><div className="traffic-bars">{charts.requestsByActor.map((item) => {
      const total = charts.requestsByActor.reduce((sum, row) => sum + row.count, 0);
      const percent = total ? item.count / total * 100 : 0;
      return <div className="traffic-bar-row" key={item.label} title={`${item.count.toLocaleString()} requests · ${percent.toFixed(1)}%`}><span>{item.label}</span><div className="track"><i style={{ width: `${Math.min(100, item.count / Math.max(1, ...charts.requestsByActor.map((row) => row.count)) * 100)}%` }} /></div><strong>{item.count.toLocaleString()} <small>{percent.toFixed(1)}%</small></strong></div>;
    })}{charts.requestsByActor.length === 0 && <p className="muted">No request data recorded.</p>}</div></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">PLATFORM MIX</p><h2>Accounts by platform</h2></div></div><RadarChart values={charts.platforms} /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">LAST 30 DAYS</p><h2>Response status mix</h2></div></div><PieChart values={charts.responseStatuses} /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">ACCOUNT DEMOGRAPHICS</p><h2>Users by country</h2></div></div><BreakdownChart values={charts.usersByCountry} /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">ACCOUNT DEMOGRAPHICS</p><h2>Age range</h2></div><span className="chart-legend purple"><i /> Users</span></div><AgeBubbleChart values={charts.usersByAge} /></section>
    <section className="panel chart-panel"><div className="panel-heading"><div><p className="eyebrow">ACCOUNT DEMOGRAPHICS</p><h2>Users by gender</h2></div></div><BreakdownChart values={charts.usersByGender} /></section>
  </div>;
}
