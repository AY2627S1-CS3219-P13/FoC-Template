"use client";

import { useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { UserApiError, userApi, type User } from "@/lib/user-api";

type Mode = "login" | "register" | "verify" | "success";
type Field = "email" | "displayName" | "password";
type PendingSignup = { email: string; registrationToken: string };

const signupKey = "foc-pending-signup";
const codeLength = 8; // The User Service uses eight digits; the wireframe's six are a placeholder.

function readPending(): PendingSignup | null {
  try {
    const value = sessionStorage.getItem(signupKey);
    return value ? JSON.parse(value) as PendingSignup : null;
  } catch {
    return null;
  }
}

export default function AuthDialog({ initialMode, onClose, onLogin }: { initialMode: "login" | "register"; onClose: () => void; onLogin: (user: User) => void }) {
  const [mode, setMode] = useState<Mode>(initialMode);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [replacementName, setReplacementName] = useState("");
  const [needsNewName, setNeedsNewName] = useState(false);
  const [touched, setTouched] = useState<Record<Field, boolean>>({ email: false, displayName: false, password: false });
  const [fieldErrors, setFieldErrors] = useState<Partial<Record<Field, string>>>({});
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const codeInputs = useRef<Array<HTMLInputElement | null>>([]);

  const emailValid = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim());
  const nameValid = displayName.trim().length >= 1 && displayName.trim().length <= 50;
  const passwordValid = password.length >= 12 && password.length <= 128;
  const canRegister = emailValid && nameValid && passwordValid;
  const canVerify = /^\d{8}$/.test(code) && (!needsNewName || replacementName.trim().length >= 1);
  const emailError = fieldErrors.email || (touched.email && !emailValid ? "Enter a valid school email address." : "");
  const nameError = fieldErrors.displayName || (touched.displayName && !nameValid ? "Use a display name between 1 and 50 characters." : "");
  const passwordError = fieldErrors.password || (touched.password && !passwordValid ? "Use a password between 12 and 128 characters." : "");

  function changeMode(next: Mode) {
    if (next === "verify") {
      const pending = readPending();
      if (!pending) {
        setMode("register");
        setError("Start a new signup to receive a verification code.");
        return;
      }
      setEmail(pending.email);
    }
    setMode(next);
    setError("");
    setMessage("");
    setFieldErrors({});
    setTouched({ email: false, displayName: false, password: false });
    setPassword("");
    if (next === "register") {
      setCode("");
      setNeedsNewName(false);
      setReplacementName("");
    }
  }

  function updateCode(index: number, raw: string) {
    const digits = raw.replace(/\D/g, "").slice(0, codeLength - index);
    const next = code.padEnd(codeLength, " ").split("");
    if (digits) {
      for (let offset = 0; offset < digits.length; offset++) next[index + offset] = digits[offset];
      codeInputs.current[Math.min(index + digits.length, codeLength - 1)]?.focus();
    } else {
      next[index] = " ";
    }
    setCode(next.join("").trimEnd());
    setError("");
  }

  function codeKeyDown(event: KeyboardEvent<HTMLInputElement>, index: number) {
    if (event.key === "Backspace") {
      event.preventDefault();
      const next = code.padEnd(codeLength, " ").split("");
      const target = next[index] === " " ? Math.max(0, index - 1) : index;
      next[target] = " ";
      setCode(next.join("").trimEnd());
      codeInputs.current[target]?.focus();
      setError("");
    } else if (event.key === "ArrowLeft" && index > 0) {
      event.preventDefault();
      codeInputs.current[index - 1]?.focus();
    } else if (event.key === "ArrowRight" && index < codeLength - 1) {
      event.preventDefault();
      codeInputs.current[index + 1]?.focus();
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const address = email.trim().toLowerCase();
      if (mode === "login") {
        const result = await userApi.login(address, password);
        onLogin(result.user);
        return;
      }
      if (mode === "register") {
        if (!canRegister) return;
        let token: string;
        let deliveryFailed = false;
        try {
          token = (await userApi.register(address, displayName.trim(), password)).registrationToken;
        } catch (failure) {
          // An email-delivery failure leaves a pending account that can receive a resent code.
          if (!(failure instanceof UserApiError) || failure.code !== "email_unavailable" || !failure.registrationToken) throw failure;
          token = failure.registrationToken;
          deliveryFailed = true;
        }
        sessionStorage.setItem(signupKey, JSON.stringify({ email: address, registrationToken: token }));
        setEmail(address);
        setPassword("");
        setCode("");
        setNeedsNewName(false);
        setReplacementName("");
        setMode("verify");
        setMessage(deliveryFailed ? "Email delivery failed. Try resending the code shortly." : "Check your inbox for the code.");
        return;
      }
      if (mode !== "verify" || !canVerify) return;
      const pending = readPending();
      if (!pending || pending.email !== address) {
        setError("This signup attempt is no longer available. Register again to get a new code.");
        return;
      }
      await userApi.verify(address, code, pending.registrationToken, needsNewName ? replacementName.trim() : undefined);
      sessionStorage.removeItem(signupKey);
      setCode("");
      setNeedsNewName(false);
      setMode("success");
    } catch (failure) {
      if (failure instanceof UserApiError && mode === "register") {
        if (failure.code === "invalid_email") setFieldErrors({ email: failure.message });
        else if (failure.code === "display_name_taken" || failure.code === "invalid_display_name") setFieldErrors({ displayName: failure.message });
        else if (failure.code === "invalid_password") setFieldErrors({ password: failure.message });
        else setError(failure.message);
      } else if (failure instanceof UserApiError && mode === "verify" && failure.code === "display_name_taken") {
        setNeedsNewName(true);
        setError("That display name was taken. Enter a different name and retry this code.");
      } else if (failure instanceof UserApiError && mode === "verify" && failure.code === "invalid_verification") {
        setError("Incorrect, expired or unusable code. Your account is still unverified.");
      } else {
        setError(failure instanceof UserApiError ? failure.message : "Could not reach the User Service. Check that it is running.");
      }
    } finally {
      setBusy(false);
    }
  }

  async function resend() {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const pending = readPending();
      if (!pending || pending.email !== email) {
        setError("Register again to start a new signup attempt.");
        return;
      }
      await userApi.resend(email);
      setCode("");
      setMessage("Check your inbox for the code.");
    } catch (failure) {
      setError(failure instanceof UserApiError ? failure.message : "Could not reach the User Service. Check that it is running.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="dialog-backdrop" onMouseDown={(event) => { if (event.target === event.currentTarget && !busy) onClose(); }}>
      <section className="dialog auth-dialog" role="dialog" aria-modal="true" aria-labelledby="auth-title">
        <div className="dialog-heading">
          <div>
            <p className="auth-overline">{mode === "login" ? "Account access" : mode === "register" ? "Create account · Step 1 of 2" : mode === "verify" ? "Email verification · Step 2 of 2" : "Account ready"}</p>
            {mode !== "success" && <h2 id="auth-title">{mode === "login" ? "Log in to FoC" : mode === "register" ? "Create your FoC account" : "Verify your school email"}</h2>}
          </div>
          <button type="button" className="close-button" onClick={onClose} disabled={busy} aria-label="Close account form">×</button>
        </div>

        {mode === "success" ? (
          <div className="auth-success">
            <div className="auth-success-card" role="status">
              <span className="auth-success-mark" aria-hidden="true">✓</span>
              <h2 id="auth-title">Email verified — account active</h2>
              <p>Your school email is verified. You can now log in to your FoC account.</p>
            </div>
            <button type="button" className="primary-button auth-submit" onClick={() => changeMode("login")}>Continue to login</button>
          </div>
        ) : (
          <>
            <p className="auth-subtitle">{mode === "login" ? "Use your verified school account." : mode === "register" ? "One account per student." : `Code sent to ${email}`}</p>
            {message && <p className="notice" role="status">{message}</p>}
            {error && mode !== "verify" && <p className="form-error" role="alert">{error}</p>}
            <form onSubmit={submit} className="auth-form" noValidate={mode === "register"}>
              {mode !== "verify" && (
                <label className="auth-field">
                  <span>School email</span>
                  <input type="email" autoComplete="email" required value={email} aria-invalid={!!emailError}
                    className={emailError ? "is-invalid" : ""} onBlur={() => setTouched((current) => ({ ...current, email: true }))}
                    onChange={(event) => { setEmail(event.target.value); setFieldErrors((current) => ({ ...current, email: "" })); }} placeholder="you@u.nus.edu" />
                  {mode === "register" && emailError && <small className="auth-inline-error">{emailError}</small>}
                </label>
              )}
              {mode === "register" && (
                <label className="auth-field">
                  <span>Display name</span>
                  <input autoComplete="nickname" required maxLength={50} value={displayName} aria-invalid={!!nameError}
                    className={nameError ? "is-invalid" : ""} onBlur={() => setTouched((current) => ({ ...current, displayName: true }))}
                    onChange={(event) => { setDisplayName(event.target.value); setFieldErrors((current) => ({ ...current, displayName: "" })); }} placeholder="How others will see your name" />
                  {nameError && <small className="auth-inline-error">{nameError}</small>}
                </label>
              )}
              {mode !== "verify" && (
                <label className="auth-field">
                  <span>Password</span>
                  <input type="password" autoComplete={mode === "login" ? "current-password" : "new-password"} required minLength={12} maxLength={128}
                    value={password} aria-invalid={mode === "register" && !!passwordError} className={mode === "register" && passwordError ? "is-invalid" : ""}
                    onBlur={() => setTouched((current) => ({ ...current, password: true }))}
                    onChange={(event) => { setPassword(event.target.value); setFieldErrors((current) => ({ ...current, password: "" })); }} />
                  {mode === "register" && <span className="auth-password-meta"><small className={passwordError ? "auth-inline-error" : ""}>{passwordError || "Minimum 12 characters"}</small><small>{password.length} / 12</small></span>}
                </label>
              )}
              {mode === "verify" && (
                <>
                  <div className="auth-code-field">
                    <span className="auth-code-label">Verification code</span>
                    <div className="auth-code-grid" role="group" aria-label="Eight-digit verification code">
                      {Array.from({ length: codeLength }, (_, index) => (
                        <input key={index} ref={(element) => { codeInputs.current[index] = element; }}
                          aria-label={`Code digit ${index + 1} of ${codeLength}`} inputMode="numeric" pattern="[0-9]*" maxLength={1}
                          autoComplete={index === 0 ? "one-time-code" : "off"} value={/\d/.test(code[index] ?? "") ? code[index] : ""}
                          onChange={(event) => updateCode(index, event.target.value)} onKeyDown={(event) => codeKeyDown(event, index)}
                          onPaste={(event) => { event.preventDefault(); updateCode(index, event.clipboardData.getData("text")); }} />
                      ))}
                    </div>
                    <small className="auth-code-hint">Enter the 8-digit code. It expires after 30 minutes.</small>
                  </div>
                  {error && <p className="form-error" role="alert">{error}</p>}
                  {needsNewName && <label className="auth-field"><span>New display name</span><input maxLength={50} required value={replacementName} onChange={(event) => setReplacementName(event.target.value)} /></label>}
                </>
              )}
              <button type="submit" className="primary-button auth-submit" disabled={busy || (mode === "register" && !canRegister) || (mode === "verify" && !canVerify)}>
                {busy ? "Please wait…" : mode === "login" ? "Log in" : mode === "register" ? "Send verification code" : "Verify"}
              </button>
            </form>
            <div className="auth-footer-actions">
              {mode === "verify" ? (
                <>
                  <button type="button" className="text-button" disabled={busy} onClick={resend}>Resend code</button>
                  <button type="button" className="text-button" disabled={busy} onClick={() => changeMode("register")}>Use another email</button>
                  <button type="button" className="text-button" disabled={busy} onClick={() => changeMode("login")}>Log in instead</button>
                </>
              ) : mode === "register" ? (
                <><span>Already have an account?</span><button type="button" className="text-button" disabled={busy} onClick={() => changeMode("login")}>Log in</button></>
              ) : (
                <><span>New to FoC?</span><button type="button" className="text-button" disabled={busy} onClick={() => changeMode("register")}>Create an account</button><button type="button" className="text-button" disabled={busy} onClick={() => changeMode("verify")}>Have a code?</button></>
              )}
            </div>
            {mode === "verify" && <div className="auth-verification-note">Your account stays unverified until the correct code is accepted. Sign-in becomes available after verification.</div>}
          </>
        )}
      </section>
    </div>
  );
}
