"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import AuthDialog from "./auth-dialog";
import Catalogue from "./catalogue";
import WalletBalance from "./wallet-balance";
import { UserApiError, userApi, type User } from "@/lib/user-api";

export default function Home() {
  const [user, setUser] = useState<User | null>(null);
  const [checking, setChecking] = useState(true);
  const [authMode, setAuthMode] = useState<"login" | "register" | null>(null);
  const [notice, setNotice] = useState("");
  const [loggingOut, setLoggingOut] = useState(false);
  const generation = useRef(0);
  const endingSession = useRef(false);

  const invalidateReads = useCallback(() => { generation.current++; }, []);
  const refreshSession = useCallback(() => {
    if (endingSession.current) return Promise.resolve();
    const requestGeneration = ++generation.current;
    return userApi.me().then(({ user }) => {
      if (requestGeneration === generation.current) setUser(user);
    }).catch((error: unknown) => {
      if (requestGeneration !== generation.current) return;
      if (error instanceof UserApiError && error.status === 401) setUser(null);
      else setNotice("Could not check your session. Please try logging in again.");
    }).finally(() => {
      if (requestGeneration === generation.current) setChecking(false);
    });
  }, []);

  useEffect(() => {
    const check = () => { if (document.visibilityState === "visible") void refreshSession(); };
    void refreshSession();
    window.addEventListener("focus", check);
    document.addEventListener("visibilitychange", check);
    return () => {
      invalidateReads();
      window.removeEventListener("focus", check);
      document.removeEventListener("visibilitychange", check);
    };
  }, [refreshSession, invalidateReads]);

  const expired = useCallback(() => {
    generation.current++;
    setUser(null);
    setChecking(false);
    setNotice("Your session has ended. Log in again to continue.");
  }, []);

  async function logout() {
    endingSession.current = true;
    generation.current++;
    setLoggingOut(true);
    try { await userApi.logout(); setUser(null); setNotice("You are logged out."); }
    catch { setNotice("Could not log out. Please retry."); }
    finally { endingSession.current = false; setLoggingOut(false); }
  }

  return <div className="site-shell">
    <header className="topbar">
      <Link className="brand" href="/" aria-label="Friend on Campus home"><span className="brand-mark">F</span><span><strong>Friend on Campus</strong><small>Campus supplier directory</small></span></Link>
      <div className="account-actions">
        {checking ? <span className="account-label">Checking session…</span> : user ? <>
          <span className="account-label" title={user.displayName}>{user.displayName} · {user.roles.includes("admin") ? "Admin" : "User"}</span>
          <WalletBalance key={user.id} />
          {user.roles.includes("admin") && <Link className="secondary-button admin-nav-link" href="/admin">Admin page</Link>}
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
        <Catalogue key={user.id} canManage={user.roles.includes("admin")} onSessionExpired={expired} onPermissionsChanged={refreshSession} /> :
        <section className="empty-state welcome-panel"><p className="eyebrow">Made for our campus</p><h2>Explore the supplier directory</h2><p>Log in with your verified school account to browse campus suppliers.</p><button className="primary-button" onClick={() => setAuthMode("login")}>Log in to browse</button></section>}
    </main>
    <footer className="footer">Friend on Campus · NUS</footer>
    {authMode && <AuthDialog initialMode={authMode} onClose={() => setAuthMode(null)} onLogin={(user) => { generation.current++; setUser(user); setChecking(false); setAuthMode(null); setNotice(""); }} />}
  </div>;
}
