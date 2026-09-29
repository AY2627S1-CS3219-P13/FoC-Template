"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { UserApiError, userApi, type User } from "@/lib/user-api";

export default function AdminSetupPage() {
  const [user, setUser] = useState<User | null>(null);
  const [checking, setChecking] = useState(true);
  const [claiming, setClaiming] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    void userApi.me().then(({ user }) => {
      if (active) setUser(user);
    }).catch(() => {
      if (active) setError("Log in with the designated, verified school account before claiming administrator access.");
    }).finally(() => {
      if (active) setChecking(false);
    });
    return () => { active = false; };
  }, []);

  async function claim() {
    setClaiming(true);
    setError("");
    try {
      const result = await userApi.claimFirstAdmin();
      setMessage(result.message);
      setUser(null);
    } catch (failure) {
      setError(failure instanceof UserApiError ? failure.message : "Administrator setup failed. Please retry.");
    } finally {
      setClaiming(false);
    }
  }

  return <div className="site-shell">
    <header className="topbar"><Link className="brand" href="/"><span className="brand-mark">F</span><span><strong>Friend on Campus</strong><small>Administration setup</small></span></Link></header>
    <main className="admin-main">
      <section className="admin-heading">
        <p className="eyebrow">One-time setup</p>
        <h1>Claim administrator access</h1>
        <p>Only the pre-designated school account can complete this step. The account must already be verified and signed in.</p>
      </section>
      <section className="admin-panel">
        {checking ? <p role="status">Checking your account…</p> : null}
        {user && !user.roles.includes("admin") && !message ? <>
          <p>Signed in as {user.email}. Claiming access ends all sessions for this account; you will need to log in again.</p>
          <button className="primary-button" disabled={claiming} onClick={() => void claim()}>{claiming ? "Claiming…" : "Claim administrator access"}</button>
        </> : null}
        {user?.roles.includes("admin") ? <p>You already have administrator access. <Link href="/admin">Open the admin page</Link>.</p> : null}
        {message ? <p className="notice" role="status">{message} <Link href="/">Return home to log in</Link>.</p> : null}
        {error ? <p className="form-error" role="alert">{error} <Link href="/">Return home</Link>.</p> : null}
      </section>
    </main>
  </div>;
}
