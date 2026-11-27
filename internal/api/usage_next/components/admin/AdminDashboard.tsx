"use client";

import { type FormEvent, useCallback, useEffect, useState } from "react";
import { LogOut, RefreshCw } from "lucide-react";
import { AlertsMenu } from "@/components/admin/alerts-menu";
import { OverviewPage } from "@/components/admin/pages/overview-page";
import { RequestsPage } from "@/components/admin/pages/requests-page";
import { AnalyticsPage } from "@/components/admin/pages/analytics-page";
import { FeedbackInbox } from "@/components/admin/pages/feedback-page";
import { AdminNotesPage } from "@/components/admin/pages/notes-page";
import { AdminProfilePage } from "@/components/admin/pages/profile-page";
import { UsersPage } from "@/components/admin/pages/users-page";
import { LibraryPage } from "@/components/admin/pages/library-page";
import { NotificationsPage } from "@/components/admin/pages/notifications-page";
import { ModeToggle } from "@/components/ModeToggle";
import { api } from "@/lib/admin-api";
import type { AdminNote, AdminProfile, Dashboard, ErrorItem, Insight, PageKey, PublicBook, RequestItem, UsageAnalytics, User } from "@/lib/admin-types";

const pages = [
  ["overview", "Overview", "⌂"], ["analytics", "Usage analytics", "▤"], ["users", "Users", "♙"],
  ["insights", "Reports & insights", "✦"], ["requests", "Requests", "↗"], ["library", "Shelve Library", "▣"],
  ["notifications", "Notifications", "◉"], ["notes", "Private notes", "▤"], ["profile", "Admin profile", "◎"],
] as const;

export default function AdminDashboard() {
  const [token, setToken] = useState("");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("");
  const [page, setPage] = useState<PageKey>("overview");
  const [dashboard, setDashboard] = useState<Dashboard | null>(null);
  const [users, setUsers] = useState<User[]>([]);
  const [errors, setErrors] = useState<ErrorItem[]>([]);
  const [insights, setInsights] = useState<Insight[]>([]);
  const [books, setBooks] = useState<PublicBook[]>([]);
  const [requests, setRequests] = useState<RequestItem[]>([]);
  const [adminNotes, setAdminNotes] = useState<AdminNote[]>([]);
  const [usageAnalytics, setUsageAnalytics] = useState<UsageAnalytics | null>(null);
  const [analyticsPeriod, setAnalyticsPeriod] = useState<UsageAnalytics["period"]>("day");
  const [adminProfile, setAdminProfile] = useState<AdminProfile | null>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);

  const loadPage = useCallback(async (currentPage: PageKey, auth: string) => {
    if (currentPage === "overview") setDashboard(await api<Dashboard>("/api/admin/dashboard", auth));
    if (currentPage === "analytics") setUsageAnalytics(await api<UsageAnalytics>(`/api/admin/analytics?period=${analyticsPeriod}`, auth));
    if (currentPage === "users") setUsers(await api<User[]>("/api/admin/users", auth));
    if (currentPage === "insights") {
      const [errorItems, insightItems] = await Promise.all([
        api<ErrorItem[]>("/api/admin/errors", auth),
        api<Insight[]>("/api/admin/reports", auth),
      ]);
      setErrors(errorItems);
      setInsights(insightItems);
    }
    if (currentPage === "library") setBooks(await api<PublicBook[]>("/api/admin/books", auth));
    if (currentPage === "requests") setRequests(await api<RequestItem[]>("/api/admin/requests?limit=200", auth));
    if (currentPage === "notifications") setUsers(await api<User[]>("/api/admin/users", auth));
    if (currentPage === "notes") setAdminNotes(await api<AdminNote[]>("/api/admin/notes", auth));
    if (currentPage === "profile") setAdminProfile(await api<AdminProfile>("/api/admin/profile", auth));
  }, [analyticsPeriod]);

  useEffect(() => {
    const existing = window.sessionStorage.getItem("shelve-admin-token");
    if (!existing) return;
    void api<{ username: string; role: string }>("/api/admin/me", existing)
      .then((me) => { setUsername(me.username); setRole(me.role); setToken(existing); })
      .catch(() => { window.sessionStorage.removeItem("shelve-admin-token"); setToken(""); });
  }, [loadPage]);

  useEffect(() => {
    if (!token) return;
    let active = true;
    const timer = window.setTimeout(() => {
      void loadPage(page, token).catch((error: unknown) => {
        if (active) setNotice(error instanceof Error ? error.message : "Could not load this section.");
      });
    }, 0);
    return () => { active = false; window.clearTimeout(timer); };
  }, [page, token, loadPage]);

  const login = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setBusy(true); setNotice("");
    try {
      const result = await api<{ token: string; username: string; role: string }>("/api/admin/login", "", {
        method: "POST", body: JSON.stringify({ username, password }),
      });
      window.sessionStorage.setItem("shelve-admin-token", result.token);
      setToken(result.token); setUsername(result.username); setRole(result.role); setPassword("");
    } catch (error) { setNotice(error instanceof Error ? error.message : "Sign in failed."); }
    finally { setBusy(false); }
  };
  const updateAdminProfile = (updated: AdminProfile & { token?: string }) => {
    if (updated.token) {
      window.sessionStorage.setItem("shelve-admin-token", updated.token);
      setToken(updated.token);
      setUsername(updated.username);
    }
    setAdminProfile(updated);
  };
  const logout = () => {
    window.sessionStorage.removeItem("shelve-admin-token");
    setToken(""); setRole("");
  };
  const refresh = async () => {
    setBusy(true);
    try { await loadPage(page, token); setNotice("Everything is up to date."); }
    catch (error) { setNotice(error instanceof Error ? error.message : "Refresh failed."); }
    finally { setBusy(false); }
  };
  if (!token) return <main className="login-screen">
    <form className="login-card" onSubmit={(event) => void login(event)}>
      <div className="brand-mark">s</div><p className="eyebrow">SHELVE CONTROL CENTER</p>
      <h1>Welcome back.</h1><p className="muted">Sign in with your administrator or developer account.</p>
      <div className="login-theme"><ModeToggle /></div>
      <label>Username<input autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} required /></label>
      <label>Password<input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
      {notice && <p className="notice error">{notice}</p>}
      <button className="button primary wide" disabled={busy}>{busy ? "Signing in…" : "Sign in"}</button>
      <small className="muted">Protected admin access · session stored for this tab only</small>
    </form>
  </main>;

  return <div className="app-shell">
    <aside className="sidebar">
      <a className="brand" href="/usage"><span className="brand-mark">s</span><span>Shelve<small>CONTROL CENTER</small></span></a>
      <div className="workspace-label">WORKSPACE</div>
      <nav>{pages.filter(([key]) => key !== "profile" || role === "admin").map(([key, label, icon]) => <button key={key} className={page === key ? "active" : ""} onClick={() => setPage(key)}><span className="nav-icon">{icon}</span>{label}{key === "insights" && dashboard?.counts.unresolvedErrors ? <i>{dashboard.counts.unresolvedErrors}</i> : null}</button>)}</nav>
      <div className="sidebar-bottom"><span className="avatar">{username.slice(0, 1).toUpperCase()}</span><div className="account-copy"><strong>{username}</strong><small>{role || "Admin"}</small></div><button className="icon-button" title="Sign out" onClick={logout}><LogOut size={16} /></button></div>
    </aside>
    <main className="main-content">
      <header className="topbar"><div><p className="eyebrow">SHELVE / {pages.find(([key]) => key === page)?.[1].toUpperCase()}</p><h1>{pages.find(([key]) => key === page)?.[1]}</h1></div><div className="top-actions"><span className="live-dot">System live</span><AlertsMenu token={token} onNavigate={setPage} /><ModeToggle /><button className="button subtle" disabled={busy} onClick={() => void refresh()}><RefreshCw size={14} className={busy ? "spin-icon" : ""} /><span>Refresh</span></button></div></header>
      {notice && <div className={`notice ${notice.includes("up to date") ? "success" : "error"}`} role="status">{notice}<button onClick={() => setNotice("")}>×</button></div>}
      {page === "overview" && <OverviewPage dashboard={dashboard} />}
      {page === "analytics" && <AnalyticsPage analytics={usageAnalytics} period={analyticsPeriod} onPeriodChange={setAnalyticsPeriod} />}
      {page === "users" && <UsersPage users={users} token={token} onSaved={() => loadPage("users", token)} />}
      {page === "insights" && <FeedbackInbox errors={errors} insights={insights} token={token} reload={() => loadPage("insights", token)} />}
      {page === "requests" && <RequestsPage items={requests} />}
      {page === "library" && <LibraryPage books={books} token={token} reload={() => loadPage("library", token)} />}
      {page === "notifications" && <NotificationsPage users={users} token={token} />}
      {page === "notes" && <AdminNotesPage notes={adminNotes} token={token} onSaved={() => loadPage("notes", token)} />}
      {page === "profile" && role === "admin" && (adminProfile
        ? <AdminProfilePage token={token} profile={adminProfile} onProfileUpdated={updateAdminProfile} />
        : <div className="empty-state">Loading admin profile…</div>)}
    </main>
  </div>;
}
