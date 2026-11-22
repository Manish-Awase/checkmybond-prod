// frontend/src/pages/LoginPage.tsx
//
// Handles the full login state machine routers/auth.go's handleLogin
// actually implements:
//   1. credentials  -- username/password, optionally with a CAPTCHA
//      widget shown once the backend reports captcha_required (crosses
//      CAPTCHA_FAIL_THRESHOLD failed attempts, default 5).
//   2. otp          -- shown when the backend reports otp_required
//      (crosses OTP_FAIL_THRESHOLD, default 10) -- password was
//      correct, a code was emailed, needs to be entered to finish.
//
// Important: failCount doesn't change between a captcha-gated attempt
// and the immediately-following OTP submission, so if captcha was
// required to get here, it's required AGAIN on the OTP submission too
// -- hCaptcha tokens are single-use, so needsCaptcha staying true means
// the widget is shown (and must be freshly completed) on every
// subsequent submission, not just the first one.
import { useState, useRef, FormEvent, useContext } from "react";
import { Link, useNavigate } from "react-router-dom";
import HCaptcha from "@hcaptcha/react-hcaptcha";
import { useAuth, LoginChallengeError } from "../context/AuthContext";
import { ThemeContext } from "../App";
import { theme, FONT_DISPLAY, FONT_BODY, FONT_IMPORT } from "../theme";

const HCAPTCHA_SITE_KEY = import.meta.env.VITE_HCAPTCHA_SITE_KEY as string | undefined;

type Stage = "credentials" | "otp";

export default function LoginPage() {
  const { login } = useAuth();
  const navigate = useNavigate();
  const { dark } = useContext(ThemeContext);
  const c = theme(dark);
  const captchaRef = useRef<HCaptcha>(null);

  const [loginVal, setLoginVal] = useState("");
  const [password, setPassword] = useState("");
  const [otpCode, setOtpCode]   = useState("");
  const [stage, setStage]       = useState<Stage>("credentials");
  const [needsCaptcha, setNeedsCaptcha] = useState(false);
  const [captchaToken, setCaptchaToken] = useState<string | null>(null);
  const [error, setError]       = useState("");
  const [info, setInfo]         = useState("");
  const [loading, setLoading]   = useState(false);
  const [showPw, setShowPw]     = useState(false);

  const resetCaptcha = () => {
    captchaRef.current?.resetCaptcha();
    setCaptchaToken(null);
  };

  const attemptLogin = async () => {
    setLoading(true);
    setError("");
    try {
      const loggedInUser = await login(loginVal.trim(), password, {
        captchaToken: needsCaptcha ? (captchaToken ?? undefined) : undefined,
        otpCode: stage === "otp" ? otpCode.trim() : undefined,
      });
      if (loggedInUser?.role === "reviewer")   navigate("/queue");
      else if (loggedInUser?.role === "admin") navigate("/dashboard");
      else if (loggedInUser?.role === "debug") navigate("/pipeline1");
      else                                     navigate("/upload");
    } catch (err) {
      if (err instanceof LoginChallengeError) {
        if (err.challenge.captchaRequired) setNeedsCaptcha(true);
        if (err.challenge.otpRequired) {
          setStage("otp");
          setInfo(err.message);
        } else {
          setError(err.message);
        }
      } else {
        setError(err instanceof Error ? err.message : "Login failed");
      }
      resetCaptcha();
    } finally {
      setLoading(false);
    }
  };

  const handleCredentialsSubmit = (e: FormEvent) => {
    e.preventDefault();
    const trimmed = loginVal.trim();
    if (!trimmed || !password.trim()) {
      setError("Enter your username/email and password.");
      return;
    }
    if (needsCaptcha && !captchaToken) {
      setError("Please complete the CAPTCHA.");
      return;
    }
    setError("");
    attemptLogin();
  };

  const handleOtpSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!otpCode.trim()) {
      setError("Enter the code we emailed you.");
      return;
    }
    if (needsCaptcha && !captchaToken) {
      setError("Please complete the CAPTCHA.");
      return;
    }
    setError("");
    attemptLogin();
  };

  const handleGoogleLogin = () => {
    // NOTE: this currently redirects into a login path that isn't fully
    // wired to real session issuance yet -- see internal/oauthusers'
    // in-memory store. Leaving the entry point in place since the OAuth
    // handshake itself works; flagging here rather than removing it
    // silently.
    window.location.href = `/api/auth/oauth/google`;
  };

  const inputStyle = {
    width: "100%", background: c.paper,
    border: `1px solid ${error ? c.flag : c.line}`,
    borderRadius: "4px", color: c.ink,
    fontFamily: "monospace", fontSize: "14px",
    padding: "10px 14px", outline: "none",
    boxSizing: "border-box" as const,
  };

  return (
    <div style={{
      minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center",
      background: c.paper, fontFamily: FONT_BODY, padding: "32px 20px",
    }}>
      <style>{FONT_IMPORT}</style>
      <div style={{ width: "100%", maxWidth: "400px", display: "flex", flexDirection: "column", gap: "20px" }}>

        <span style={{ fontFamily: FONT_DISPLAY, fontWeight: 700, fontSize: "18px", color: c.ink }}>BondQi</span>

        <div style={{ background: c.surface, border: `1px solid ${c.line}`, borderRadius: "6px", padding: "28px" }}>

          {stage === "credentials" && (
            <>
              <h1 style={{ fontFamily: FONT_DISPLAY, fontSize: "20px", fontWeight: 700, color: c.ink, margin: "0 0 4px" }}>
                Sign in
              </h1>
              <p style={{ fontSize: "13px", color: c.muted, margin: "0 0 22px" }}>
                Use your username or email address
              </p>

              <form onSubmit={handleCredentialsSubmit} style={{ display: "flex", flexDirection: "column", gap: "14px" }}>
                <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                  <label style={{ fontSize: "12px", color: c.muted }}>Username or email</label>
                  <input
                    type="text" autoComplete="username" autoFocus
                    value={loginVal} onChange={e => { setLoginVal(e.target.value); setError(""); }}
                    placeholder="username or you@example.com"
                    disabled={loading}
                    style={inputStyle}
                  />
                </div>

                <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                  <div style={{ display: "flex", justifyContent: "space-between" }}>
                    <label style={{ fontSize: "12px", color: c.muted }}>Password</label>
                    <Link to="/forgot-password" style={{ fontSize: "12px", color: c.verified, textDecoration: "none", fontWeight: 600 }}>
                      Forgot password?
                    </Link>
                  </div>
                  <div style={{ position: "relative" }}>
                    <input
                      type={showPw ? "text" : "password"} autoComplete="current-password"
                      value={password} onChange={e => { setPassword(e.target.value); setError(""); }}
                      placeholder="••••••••" disabled={loading}
                      style={{ ...inputStyle, paddingRight: "44px" }}
                    />
                    <button type="button" tabIndex={-1} onClick={() => setShowPw(v => !v)}
                      style={{ position: "absolute", right: 0, top: 0, bottom: 0, width: "44px",
                        background: "none", border: "none", cursor: "pointer", opacity: 0.6 }}>
                      {showPw ? "hide" : "show"}
                    </button>
                  </div>
                </div>

                {needsCaptcha && (
                  <div>
                    {HCAPTCHA_SITE_KEY ? (
                      <HCaptcha
                        ref={captchaRef}
                        sitekey={HCAPTCHA_SITE_KEY}
                        onVerify={setCaptchaToken}
                        onExpire={() => setCaptchaToken(null)}
                        theme={dark ? "dark" : "light"}
                      />
                    ) : (
                      <p style={{ fontSize: "12px", color: c.flag }}>
                        CAPTCHA is required but VITE_HCAPTCHA_SITE_KEY is not set — contact support.
                      </p>
                    )}
                  </div>
                )}

                {error && <ErrorBox flag={c.flag}>{error}</ErrorBox>}

                <SubmitButton ink={c.ink} paper={c.paper} loading={loading} label="Sign in" loadingLabel="Signing in…" />

                <Divider line={c.line} muted={c.muted} />

                <button type="button" onClick={handleGoogleLogin} disabled={loading} style={{
                  background: c.paper, border: `1px solid ${c.line}`, borderRadius: "4px",
                  color: c.ink, fontFamily: FONT_BODY, fontSize: "14px", fontWeight: 600,
                  padding: "11px", cursor: loading ? "default" : "pointer",
                  display: "flex", alignItems: "center", justifyContent: "center", gap: "10px",
                }}>
                  <GoogleIcon />
                  Continue with Google
                </button>

                <p style={{ textAlign: "center", fontSize: "13px", color: c.muted, margin: 0 }}>
                  Don't have an account?{" "}
                  <Link to="/register" style={{ color: c.verified, textDecoration: "none", fontWeight: 600 }}>Create one</Link>
                </p>
              </form>
            </>
          )}

          {stage === "otp" && (
            <>
              <h1 style={{ fontFamily: FONT_DISPLAY, fontSize: "20px", fontWeight: 700, color: c.ink, margin: "0 0 4px" }}>
                Check your email
              </h1>
              <p style={{ fontSize: "13px", color: c.muted, margin: "0 0 22px" }}>
                {info || "We sent a verification code to your email — enter it below."}
              </p>

              <form onSubmit={handleOtpSubmit} style={{ display: "flex", flexDirection: "column", gap: "14px" }}>
                <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
                  <label style={{ fontSize: "12px", color: c.muted }}>Verification code</label>
                  <input
                    type="text" inputMode="numeric" autoFocus
                    value={otpCode} onChange={e => { setOtpCode(e.target.value); setError(""); }}
                    placeholder="123456" disabled={loading}
                    style={{ ...inputStyle, textAlign: "center", fontSize: "18px", letterSpacing: "4px" }}
                  />
                </div>

                {needsCaptcha && (
                  <div>
                    {HCAPTCHA_SITE_KEY && (
                      <HCaptcha
                        ref={captchaRef}
                        sitekey={HCAPTCHA_SITE_KEY}
                        onVerify={setCaptchaToken}
                        onExpire={() => setCaptchaToken(null)}
                        theme={dark ? "dark" : "light"}
                      />
                    )}
                  </div>
                )}

                {error && <ErrorBox flag={c.flag}>{error}</ErrorBox>}

                <SubmitButton ink={c.ink} paper={c.paper} loading={loading} label="Verify" loadingLabel="Verifying…" />

                <p style={{ textAlign: "center", fontSize: "13px", color: c.muted, margin: 0 }}>
                  <button type="button" onClick={() => { setStage("credentials"); setOtpCode(""); setError(""); }}
                    style={{ background: "none", border: "none", color: c.verified, fontWeight: 600, cursor: "pointer", fontSize: "13px" }}>
                    Back to sign in
                  </button>
                </p>
              </form>
            </>
          )}
        </div>
      </div>
    </div>
  );
}

function ErrorBox({ flag, children }: { flag: string; children: React.ReactNode }) {
  return (
    <div style={{ background: "rgba(156,66,33,.08)", border: `1px solid ${flag}55`,
      borderRadius: "4px", padding: "10px 14px", color: flag, fontSize: "13px" }}>
      {children}
    </div>
  );
}

function SubmitButton({ ink, paper, loading, label, loadingLabel }: {
  ink: string; paper: string; loading: boolean; label: string; loadingLabel: string;
}) {
  return (
    <button type="submit" disabled={loading} style={{
      background: ink, border: "none", borderRadius: "4px", color: paper,
      fontFamily: FONT_BODY, fontSize: "14px", fontWeight: 600,
      padding: "12px", cursor: loading ? "default" : "pointer", opacity: loading ? 0.6 : 1,
    }}>
      {loading ? loadingLabel : label}
    </button>
  );
}

function Divider({ line, muted }: { line: string; muted: string }) {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
      <div style={{ flex: 1, height: "1px", background: line }} />
      <span style={{ fontSize: "11px", color: muted }}>or</span>
      <div style={{ flex: 1, height: "1px", background: line }} />
    </div>
  );
}

function GoogleIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 48 48" aria-hidden="true">
      <path fill="#FFC107" d="M43.6 20.5H42V20H24v8h11.3C33.7 32.9 29.3 36 24 36c-6.6 0-12-5.4-12-12s5.4-12 12-12c3.1 0 5.9 1.2 8 3.1l5.7-5.7C34.5 6.1 29.5 4 24 4 12.9 4 4 12.9 4 24s8.9 20 20 20 20-8.9 20-20c0-1.3-.1-2.7-.4-3.5z"/>
      <path fill="#FF3D00" d="M6.3 14.7l6.6 4.8C14.6 15.9 18.9 13 24 13c3.1 0 5.9 1.2 8 3.1l5.7-5.7C34.5 6.1 29.5 4 24 4 16.3 4 9.6 8.3 6.3 14.7z"/>
      <path fill="#4CAF50" d="M24 44c5.4 0 10.3-2.1 14-5.5l-6.5-5.4C29.4 34.9 26.8 36 24 36c-5.3 0-9.7-3.4-11.3-8.1l-6.5 5C9.5 39.6 16.2 44 24 44z"/>
      <path fill="#1976D2" d="M43.6 20.5H42V20H24v8h11.3c-.8 2.3-2.3 4.3-4.2 5.7l6.5 5.4C40.5 36.7 44 30.9 44 24c0-1.3-.1-2.7-.4-3.5z"/>
    </svg>
  );
}
