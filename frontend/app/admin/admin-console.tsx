"use client";

import Link from "next/link";
import { FormEvent, useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { UserApiError, userApi, type RoleEvent, type User } from "@/lib/user-api";
import Modal from "@/app/modal";

const checks = [
  { label: "Gateway", service: "Gateway", path: "/healthz" },
  { label: "Current user", service: "User Service", path: "/api/v1/users/me" },
  { label: "Suppliers", service: "Supplier Service", path: "/api/v1/suppliers?pageSize=5" },
  { label: "User accounts", service: "User Service", path: "/api/v1/admin/users?pageSize=20" },
] as const;

type CheckResult = { status: number; duration: number; body: string; totalPages?: number };

export default function AdminConsole({ admin }: { admin: User }) {
  const router = useRouter();
  const [authorized, setAuthorized] = useState(false);
  const [email, setEmail] = useState("");
  const [target, setTarget] = useState<User | null>(null);
  const [searching, setSearching] = useState(false);
  const [changingRole, setChangingRole] = useState(false);
  const [pendingRole, setPendingRole] = useState<"user" | "admin" | null>(null);
  const [currentPassword, setCurrentPassword] = useState("");
  const [confirmError, setConfirmError] = useState("");
  const [roleMessage, setRoleMessage] = useState("");
  const [roleError, setRoleError] = useState("");
  const [events, setEvents] = useState<RoleEvent[] | null>(null);
  const [eventsBusy, setEventsBusy] = useState(false);
  const [eventsError, setEventsError] = useState("");
  const [selectedCheck, setSelectedCheck] = useState<number>(0);
  const [checking, setChecking] = useState(false);
  const [checkResult, setCheckResult] = useState<CheckResult | null>(null);
  const [checkError, setCheckError] = useState("");
  const [userPage, setUserPage] = useState(1);
  const usersCheck = checks[selectedCheck].label === "User accounts";
  const checkPath = usersCheck ? `${checks[selectedCheck].path}&page=${userPage}` : checks[selectedCheck].path;

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

  function openRoleChange(role: "user" | "admin") {
    if (!target || target.id === admin.id) return;
    setPendingRole(role);
    setCurrentPassword("");
    setConfirmError("");
  }

  function closeRoleChange() {
    if (changingRole) return;
    setPendingRole(null);
    setCurrentPassword("");
    setConfirmError("");
  }

  async function submitRoleChange(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!target || !pendingRole || !currentPassword) return;
    const role = pendingRole;
    setChangingRole(true);
    setRoleMessage("");
    setRoleError("");
    setConfirmError("");
    try {
      const result = await userApi.changeUserRole(target.id, role, currentPassword);
      setTarget({ ...target, roles: result.roles });
      setRoleMessage(`${target.email} is now ${role === "admin" ? "an admin" : "a user"}. Their sessions ended; they must log in again.`);
      setEvents(null);
      setPendingRole(null);
    } catch (error) {
      setConfirmError(error instanceof UserApiError ? error.message : "Could not change this role. Please retry.");
    } finally {
      setCurrentPassword("");
      setChangingRole(false);
    }
  }

  async function loadEvents() {
    setEventsBusy(true);
    setEventsError("");
    try {
      setEvents((await userApi.roleEvents()).events);
    } catch (error) {
      setEventsError(error instanceof UserApiError ? error.message : "Could not load role history. Please retry.");
    } finally {
      setEventsBusy(false);
    }
  }

  async function runCheck(page = userPage) {
    const check = checks[selectedCheck];
    const path = check.label === "User accounts" ? `${check.path}&page=${page}` : check.path;
    setChecking(true);
    setUserPage(page);
    setCheckResult(null);
    setCheckError("");
    const started = performance.now();
    try {
      const response = await fetch(path, { method: "GET", credentials: "include", cache: "no-store" });
      const raw = await response.text();
      let body = raw;
      let totalPages: number | undefined;
      try {
        const data = JSON.parse(raw);
        body = JSON.stringify(data, null, 2);
        if (check.label === "User accounts" && response.ok) totalPages = data.totalPages;
      } catch { /* Gateway health is plain text. */ }
      setCheckResult({ status: response.status, duration: Math.round(performance.now() - started), body, totalPages });
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
          <h2 id="role-title">Manage user roles</h2>
          <p>Search by exact school email. Only verified accounts can have their role changed.</p>
          <form className="admin-form" onSubmit={search}>
            <label htmlFor="admin-email"><span>School email</span><input id="admin-email" type="email" value={email} disabled={searching || changingRole} onChange={(event) => { setEmail(event.target.value); setTarget(null); setRoleError(""); setRoleMessage(""); }} placeholder="student@u.nus.edu" required /></label>
            <button className="primary-button" disabled={searching}>{searching ? "Searching…" : "Find user"}</button>
          </form>
          {roleError && <p className="form-error" role="alert">{roleError}</p>}
          {roleMessage && <p className="notice" role="status">{roleMessage}</p>}
          {target && <div className="admin-user-result">
            <strong>{target.displayName}</strong><span>{target.email}</span><span>Current role: {target.roles.includes("admin") ? "Admin" : "User"}</span>
            {target.id === admin.id ? <p>You cannot change your own role.</p> : target.roles.includes("admin") ? <button className="danger-button" onClick={() => openRoleChange("user")}>Remove admin access</button> : <button className="primary-button" onClick={() => openRoleChange("admin")}>Promote to admin</button>}
          </div>}
        </section>
        <section className="admin-panel" aria-labelledby="audit-title">
          <h2 id="audit-title">Role change history</h2>
          <p>Recent bootstrap, administrator and operator changes. Only administrators can read this history.</p>
          <button className="secondary-button" disabled={eventsBusy} onClick={() => void loadEvents()}>{eventsBusy ? "Loading…" : "Load recent changes"}</button>
          {eventsError && <p className="form-error" role="alert">{eventsError}</p>}
          {events && (events.length ? <ul className="admin-event-list">{events.map(event => <li key={event.id}>
            <strong>{event.actorLabel}</strong> ({event.actorKind}) changed <strong>{event.targetEmail}</strong> from {event.previousRole} to {event.newRole}.
            <span> {new Date(event.occurredAt).toLocaleString()}</span>{event.reason ? <p>Reason: {event.reason}</p> : null}
          </li>)}</ul> : <p>No role changes recorded yet.</p>)}
        </section>
        <section className="admin-panel" aria-labelledby="checks-title">
          <h2 id="checks-title">Service checks</h2>
          <p>Run one preset GET request and inspect its real response. A gateway result alone does not prove every service is healthy.</p>
          <div className="admin-form">
            <label htmlFor="service-check"><span>Request preset</span><select id="service-check" value={selectedCheck} disabled={checking} onChange={(event) => { setSelectedCheck(Number(event.target.value)); setUserPage(1); setCheckResult(null); setCheckError(""); }}>{checks.map((check, index) => <option key={check.path} value={index}>{check.label}</option>)}</select></label>
            <button className="primary-button" disabled={checking} onClick={() => void runCheck()}>{checking ? "Running…" : "Run GET"}</button>
          </div>
          <p className="admin-check-path">{checks[selectedCheck].service} · GET {checkPath}</p>
          {checkError && <p className="form-error" role="alert">{checkError}</p>}
          {checkResult && <div className="admin-response" role="status"><div><strong>HTTP {checkResult.status}</strong><span>{checkResult.duration} ms</span></div><pre>{checkResult.body}</pre></div>}
          {usersCheck && checkResult?.totalPages && checkResult.totalPages > 1 && <div className="admin-pagination"><button className="secondary-button" disabled={checking || userPage === 1} onClick={() => void runCheck(userPage - 1)}>Previous</button><span>Page {userPage} of {checkResult.totalPages}</span><button className="secondary-button" disabled={checking || userPage >= checkResult.totalPages} onClick={() => void runCheck(userPage + 1)}>Next</button></div>}
          <p className="admin-pending">Credit and Order checks can be added when those services have gateway routes. A failed application request does not always mean a service is down.</p>
        </section>
      </div>
    </main>
    {pendingRole && target && <Modal titleId="role-confirm-title" className="confirm-dialog" busy={changingRole} onClose={closeRoleChange}>
      <h2 id="role-confirm-title">{pendingRole === "admin" ? "Promote to admin" : "Remove admin access"}</h2>
      <p>Change {target.email} to {pendingRole}? Their current sessions will end. Enter your password to confirm.</p>
      <form onSubmit={submitRoleChange}>
        <label htmlFor="admin-current-password"><span>Your current password</span><input id="admin-current-password" type="password" autoComplete="current-password" value={currentPassword} onChange={event => setCurrentPassword(event.target.value)} required /></label>
        {confirmError && <p className="form-error" role="alert">{confirmError}</p>}
        <div className="detail-actions"><button type="button" className="secondary-button" disabled={changingRole} onClick={closeRoleChange}>Cancel</button><button type="submit" className={pendingRole === "admin" ? "primary-button" : "danger-button"} disabled={changingRole || !currentPassword}>{changingRole ? "Saving…" : "Confirm role change"}</button></div>
      </form>
    </Modal>}
  </div>;
}
