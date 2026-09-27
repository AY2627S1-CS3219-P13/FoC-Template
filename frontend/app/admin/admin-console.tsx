"use client";

import Link from "next/link";
import { FormEvent, useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { UserApiError, userApi, type User } from "@/lib/user-api";

const checks = [
  { label: "Gateway", service: "Gateway", path: "/healthz" },
  { label: "Current user", service: "User Service", path: "/api/v1/users/me" },
  { label: "Suppliers", service: "Supplier Service", path: "/api/v1/suppliers?pageSize=5" },
  { label: "Locations", service: "Supplier Service", path: "/api/v1/locations" },
] as const;

type CheckResult = { status: number; duration: number; body: string };

export default function AdminConsole({ admin }: { admin: User }) {
  const router = useRouter();
  const [authorized, setAuthorized] = useState(false);
  const [email, setEmail] = useState("");
  const [target, setTarget] = useState<User | null>(null);
  const [searching, setSearching] = useState(false);
  const [promoting, setPromoting] = useState(false);
  const [roleMessage, setRoleMessage] = useState("");
  const [roleError, setRoleError] = useState("");
  const [selectedCheck, setSelectedCheck] = useState<number>(0);
  const [checking, setChecking] = useState(false);
  const [checkResult, setCheckResult] = useState<CheckResult | null>(null);
  const [checkError, setCheckError] = useState("");

  const recheckAccess = useCallback(async () => {
    try {
      const { user } = await userApi.me();
      if (user.id === admin.id && user.roles.includes("admin")) {
        setAuthorized(true);
        return;
      }
    } catch { /* Fail closed if the session cannot be checked. */ }
    setAuthorized(false);
    router.replace("/");
  }, [admin.id, router]);

  useEffect(() => {
    const initialCheck = window.setTimeout(() => void recheckAccess(), 0);
    const onVisible = () => { if (document.visibilityState === "visible") void recheckAccess(); };
    window.addEventListener("focus", onVisible);
    document.addEventListener("visibilitychange", onVisible);
    return () => { window.clearTimeout(initialCheck); window.removeEventListener("focus", onVisible); document.removeEventListener("visibilitychange", onVisible); };
  }, [recheckAccess]);

  async function search(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSearching(true);
    setTarget(null);
    setRoleMessage("");
    setRoleError("");
    try {
      const result = await userApi.findUserByEmail(email.trim());
      setTarget(result.user);
    } catch (error) {
      setRoleError(error instanceof UserApiError ? error.message : "Could not search for that user. Please retry.");
    } finally {
      setSearching(false);
    }
  }

  async function promote() {
    if (!target || target.id === admin.id || target.roles.includes("admin")) return;
    if (!window.confirm(`Promote ${target.email} to admin? Their current sessions will end.`)) return;
    setPromoting(true);
    setRoleMessage("");
    setRoleError("");
    try {
      const result = await userApi.promoteUser(target.id);
      setTarget({ ...target, roles: result.roles });
      setRoleMessage(`${target.email} is now an admin. They must log in again.`);
    } catch (error) {
      setRoleError(error instanceof UserApiError ? error.message : "Could not promote this user. Please retry.");
    } finally {
      setPromoting(false);
    }
  }

  async function runCheck() {
    const check = checks[selectedCheck];
    setChecking(true);
    setCheckResult(null);
    setCheckError("");
    const started = performance.now();
    try {
      const response = await fetch(check.path, { method: "GET", credentials: "include", cache: "no-store" });
      const raw = await response.text();
      let body = raw;
      try { body = JSON.stringify(JSON.parse(raw), null, 2); } catch { /* Gateway health is plain text. */ }
      setCheckResult({ status: response.status, duration: Math.round(performance.now() - started), body });
    } catch {
      setCheckError("Could not reach the gateway. Check that the local services are running.");
    } finally {
      setChecking(false);
    }
  }

  if (!authorized) return <p className="empty-state" role="status">Checking admin access…</p>;

  return <div className="site-shell">
    <header className="topbar">
      <Link className="brand" href="/"><span className="brand-mark">F</span><span><strong>Friend on Campus</strong><small>Administration</small></span></Link>
      <div className="account-actions"><span className="account-label">{admin.displayName} · Admin</span><Link className="secondary-button admin-nav-link" href="/">Back to catalogue</Link></div>
    </header>
    <main className="admin-main">
      <section className="admin-heading"><p className="eyebrow">Administration</p><h1>Admin page</h1><p>Look up a verified user to grant admin access, or run a read-only request through the gateway.</p></section>
      <div className="admin-grid">
        <section className="admin-panel" aria-labelledby="role-title">
          <h2 id="role-title">Promote a user</h2>
          <p>Search by exact school email. Only verified accounts can be promoted.</p>
          <form className="admin-form" onSubmit={search}>
            <label htmlFor="admin-email"><span>School email</span><input id="admin-email" type="email" value={email} disabled={searching || promoting} onChange={(event) => { setEmail(event.target.value); setTarget(null); setRoleError(""); setRoleMessage(""); }} placeholder="student@u.nus.edu" required /></label>
            <button className="primary-button" disabled={searching}>{searching ? "Searching…" : "Find user"}</button>
          </form>
          {roleError && <p className="form-error" role="alert">{roleError}</p>}
          {roleMessage && <p className="notice" role="status">{roleMessage}</p>}
          {target && <div className="admin-user-result">
            <strong>{target.displayName}</strong><span>{target.email}</span><span>Current role: {target.roles.includes("admin") ? "Admin" : "User"}</span>
            {target.id === admin.id ? <p>You cannot change your own role.</p> : target.roles.includes("admin") ? <p>This user is already an admin.</p> : <button className="primary-button" disabled={promoting} onClick={promote}>{promoting ? "Promoting…" : "Promote to admin"}</button>}
          </div>}
        </section>
        <section className="admin-panel" aria-labelledby="checks-title">
          <h2 id="checks-title">Service checks</h2>
          <p>Run one preset GET request and inspect its real response. A gateway result alone does not prove every service is healthy.</p>
          <div className="admin-form">
            <label htmlFor="service-check"><span>Request preset</span><select id="service-check" value={selectedCheck} disabled={checking} onChange={(event) => { setSelectedCheck(Number(event.target.value)); setCheckResult(null); setCheckError(""); }}>{checks.map((check, index) => <option key={check.path} value={index}>{check.label}</option>)}</select></label>
            <button className="primary-button" disabled={checking} onClick={runCheck}>{checking ? "Running…" : "Run GET"}</button>
          </div>
          <p className="admin-check-path">{checks[selectedCheck].service} · GET {checks[selectedCheck].path}</p>
          {checkError && <p className="form-error" role="alert">{checkError}</p>}
          {checkResult && <div className="admin-response" role="status"><div><strong>HTTP {checkResult.status}</strong><span>{checkResult.duration} ms</span></div><pre>{checkResult.body}</pre></div>}
          <p className="admin-pending">Credit and Order checks can be added when those services have gateway routes. A failed application request does not always mean a service is down.</p>
        </section>
      </div>
    </main>
  </div>;
}
