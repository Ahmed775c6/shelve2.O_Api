"use client";

import Image from "next/image";
import { useRef, useState, type ChangeEvent, type FormEvent } from "react";
import { Camera, KeyRound, LockKeyhole, Save, ShieldCheck, UserRound } from "lucide-react";
import { api } from "@/lib/admin-api";
import type { AdminProfile } from "@/lib/admin-types";

async function prepareAvatar(file: File): Promise<string> {
  if (!file.type.startsWith("image/") || file.size > 5 * 1024 * 1024) {
    throw new Error("Choose an image smaller than 5 MiB.");
  }
  const image = await createImageBitmap(file);
  try {
    const scale = Math.min(1, 256 / Math.max(image.width, image.height));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(image.width * scale));
    canvas.height = Math.max(1, Math.round(image.height * scale));
    const context = canvas.getContext("2d");
    if (!context) throw new Error("Could not prepare the profile image.");
    context.drawImage(image, 0, 0, canvas.width, canvas.height);
    return canvas.toDataURL("image/webp", 0.82);
  } finally {
    image.close();
  }
}

export function AdminProfilePage({ token, profile, onProfileUpdated }: {
  token: string;
  profile: AdminProfile | null;
  onProfileUpdated: (profile: AdminProfile & { token?: string }) => void;
}) {
  const [username, setUsername] = useState(profile?.username || "");
  const [avatarDataUrl, setAvatarDataUrl] = useState(profile?.avatarDataUrl || "");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [passwordBusy, setPasswordBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const avatarRef = useRef<HTMLInputElement>(null);

  const selectAvatar = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = "";
    if (!file) return;
    setError("");
    try { setAvatarDataUrl(await prepareAvatar(file)); }
    catch (failure) { setError(failure instanceof Error ? failure.message : "Could not read the profile image."); }
  };

  const save = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true); setError(""); setNotice("");
    try {
      const updated = await api<AdminProfile & { token: string }>("/api/admin/profile", token, {
        method: "PATCH",
        body: JSON.stringify({ username: username.trim(), avatarDataUrl }),
      });
      onProfileUpdated(updated);
      setNotice("Admin profile saved.");
    } catch (failure) { setError(failure instanceof Error ? failure.message : "Could not save admin profile."); }
    finally { setBusy(false); }
  };

  const changePassword = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault(); setError(""); setNotice("");
    if (newPassword.length < 12 || newPassword.length > 72) {
      setError("New password must be between 12 and 72 characters.");
      return;
    }
    if (newPassword !== confirmPassword) {
      setError("The password confirmation does not match.");
      return;
    }
    setPasswordBusy(true);
    try {
      await api("/api/admin/profile/password", token, {
        method: "PATCH",
        body: JSON.stringify({ newPassword }),
      });
      setNewPassword(""); setConfirmPassword("");
      setNotice("Admin password changed.");
    } catch (failure) { setError(failure instanceof Error ? failure.message : "Could not change admin password."); }
    finally { setPasswordBusy(false); }
  };

  return <div className="profile-page-grid">
    <section className="panel admin-profile-panel">
      <div className="panel-heading"><div><p className="eyebrow">ADMINISTRATOR</p><h2>Profile settings</h2><p className="muted">Update the account used to access the Shelve control center.</p></div></div>
      {error && <div className="notice error">{error}</div>}
      {notice && <div className="notice success">{notice}</div>}
      <form className="admin-profile-form" onSubmit={(event) => void save(event)}>
        <div className="admin-avatar-row">
          <div className="admin-profile-avatar">{avatarDataUrl ? <Image src={avatarDataUrl} alt="Admin profile" width={64} height={64} unoptimized /> : <UserRound size={26} />}</div>
          <div><strong>Profile photo</strong><small>Private · PNG, JPEG, or WebP · 5 MiB max</small><div className="admin-avatar-actions">
            <button type="button" className="button subtle" onClick={() => avatarRef.current?.click()}><Camera size={14} />Choose photo</button>
            {avatarDataUrl && <button type="button" className="button subtle" onClick={() => setAvatarDataUrl("")}>Remove</button>}
          </div><input ref={avatarRef} hidden type="file" accept="image/png,image/jpeg,image/webp" onChange={(event) => void selectAvatar(event)} /></div>
        </div>
        <label>Username<input value={username} onChange={(event) => setUsername(event.target.value)} minLength={2} maxLength={64} pattern="[A-Za-z0-9._-]+" required /></label>
        <label>Email address<input value={profile?.email || "ahmed.chouikh2020@gmail.com"} readOnly aria-readonly="true" /><small>This administrator email is fixed and cannot be changed.</small></label>
        <button className="button primary" disabled={busy || !username.trim()}><Save size={14} />{busy ? "Saving…" : "Save profile"}</button>
      </form>
    </section>
    <section className="panel admin-profile-panel">
      <div className="panel-heading"><div><p className="eyebrow">ACCOUNT SECURITY</p><h2>Change password</h2><p className="muted">Every admin change is confirmed with your current password.</p></div><i className="panel-icon"><ShieldCheck size={17} /></i></div>
      <form className="admin-password-form" onSubmit={(event) => void changePassword(event)}>
        <label>New password<div className="password-field"><KeyRound size={15} /><input type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} minLength={12} maxLength={72} required /></div></label>
        <label>Confirm new password<div className="password-field"><LockKeyhole size={15} /><input type="password" autoComplete="new-password" value={confirmPassword} onChange={(event) => setConfirmPassword(event.target.value)} minLength={12} maxLength={72} required /></div></label>
        <button className="button primary" disabled={passwordBusy}><ShieldCheck size={14} />{passwordBusy ? "Updating…" : "Change password"}</button>
      </form>
    </section>
    <section className="panel admin-profile-panel admin-profile-facts">
      <div className="panel-heading"><div><p className="eyebrow">SESSION DETAILS</p><h2>Admin activity and platform</h2></div></div>
      <div className="profile-facts-grid">
        <div><small>Admin IP address</small><strong className="mono">{profile?.ipAddress || "Not recorded yet"}</strong></div>
        <div><small>Admin browser / platform</small><strong>{profile?.platform || "Unavailable"}</strong></div>
        <div><small>API server platform</small><strong>{profile?.serverPlatform || "Unavailable"}</strong></div>
        <div><small>Profile last updated</small><strong>{dateLabel(profile?.updatedAt)}</strong></div>
      </div>
      <p className="request-privacy-note">The IP is the connected network peer recorded by the API. Forwarded proxy headers are not trusted unless the server is configured to do so.</p>
    </section>
  </div>;
}

function dateLabel(value?: string) {
  if (!value) return "Not available";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? "Not available" : parsed.toLocaleString();
}
