"use client";
import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import AuthDialog from "./auth-dialog";
import Catalogue from "./catalogue";
import { UserApiError, userApi, type User } from "@/lib/user-api";

export default function Home() {
  const [user, setUser] = useState<User | null>(null);
  const [checking, setChecking] = useState(true);
  const [authMode, setAuthMode] = useState<"login" | "register" | null>(null);
  const [notice, setNotice] = useState("");
  const [loggingOut, setLoggingOut] = useState(false);

  useEffect(() => {
    let active = true;
    userApi.me().then(({ user }) => { if (active) setUser(user); }).catch((error: unknown) => {
      if (active && !(error instanceof UserApiError && error.status === 401)) setNotice("Could not check your session. Please try logging in.");
    }).finally(() => { if (active) setChecking(false); });
    return () => { active = false; };
  }, []);

  const expired = useCallback(() => {
    setUser(null);
    setNotice("Your session has ended. Log in again to continue.");
  }, []);

  async function logout() {
    setLoggingOut(true);
    try { await userApi.logout(); setUser(null); setNotice("You are logged out."); }
    catch { setNotice("Could not log out. Please retry."); }
    finally { setLoggingOut(false); }
  }

  return <div className="site-shell">
    <header className="topbar">
      <Link className="brand" href="/" aria-label="Friend on Campus home"><span className="brand-mark">F</span><span><strong>Friend on Campus</strong><small>Campus supplier directory</small></span></Link>
      <div className="account-actions">
        {checking ? <span className="account-label">Checking session…</span> : user ? <>
          <span className="account-label" title={user.displayName}>{user.displayName} · {user.roles.includes("admin") ? "Admin" : "User"}</span>
          <button className="secondary-button" disabled={loggingOut} onClick={logout}>{loggingOut ? "Logging out…" : "Log out"}</button>
        </> : <>
          <button className="secondary-button" onClick={() => setAuthMode("login")}>Log in</button>
          <button className="primary-button" onClick={() => setAuthMode("register")}>Sign up</button>
        </>}
      </div>
    </header>
    <main>
      <section className="hero catalogue-hero">
        <div><p className="eyebrow">Your campus, a little closer</p><h1>Find your next campus stop.</h1><p className="hero-copy">Food, printing and everyday essentials. Explore suppliers across NUS and find the details you need before you go.</p></div>
        <span className="hero-tag">Friend on Campus · NUS</span>
      </section>
      {notice && <p className="notice account-notice" role="status">{notice}</p>}
      {checking ? <p className="empty-state" role="status">Checking your session…</p> : user ?
        <Catalogue key={user.id} onSessionExpired={expired} /> :
        <section className="empty-state welcome-panel"><p className="eyebrow">Made for our campus</p><h2>Explore the supplier directory</h2><p>Log in with your verified school account to browse campus suppliers.</p><button className="primary-button" onClick={() => setAuthMode("login")}>Log in to browse</button></section>}
    </main>
    <footer className="footer">Friend on Campus · NUS</footer>
    {authMode && <AuthDialog initialMode={authMode} onClose={() => setAuthMode(null)} onLogin={(user) => { setUser(user); setAuthMode(null); setNotice(""); }} />}
  </div>;
}
