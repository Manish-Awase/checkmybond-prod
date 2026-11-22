// frontend/src/pages/OAuthCallbackPage.tsx
//
// Route: /oauth-callback
// The backend's /auth/google/callback redirects here with ?token=...
// after creating (or finding) the user's account via their Google profile.
//
// Previously wrote localStorage "authToken"/"authUser" directly -- two
// keys nothing else in the app ever read (AuthContext stores a single
// "hitl_auth" JSON blob). That meant a successful Google sign-in left
// the React app still believing the user was logged out. Now goes
// through AuthContext.setSession, the same persistence path login() and
// register() use.
//
// KNOWN LIMITATION (backend, not this file): internal/oauthusers is
// still an in-memory stub whose session token isn't an RS256 JWT the
// gateway can verify, so protected routes will reject it even once this
// page stores it correctly. This page is correct; the backend half
// still needs real Postgres-backed user lookup and real token issuance.
import { useEffect, useState, useContext } from "react";
import { useNavigate, useSearchParams, Link } from "react-router-dom";
import { oauthSessionRequest } from "../lib/api";
import { useAuth, AuthUser } from "../context/AuthContext";
import { ThemeContext } from "../App";
import { theme, FONT_DISPLAY, FONT_BODY, FONT_IMPORT } from "../theme";

export default function OAuthCallbackPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [error, setError] = useState("");
  const { setSession } = useAuth();
  const { dark } = useContext(ThemeContext);
  const c = theme(dark);

  useEffect(() => {
    const token = searchParams.get("token");
    if (!token) {
      setError("Missing authentication token.");
      return;
    }

    (async () => {
      try {
        const res = await oauthSessionRequest(token);
        if (!res.ok) throw new Error("Session lookup failed");
        const profile = await res.json();

        const authUser: AuthUser = {
          user_id:      profile.user_id ?? profile.id,
          username:     profile.username,
          email:        profile.email,
          role:         profile.role ?? "analyst",
          access_token: token,
        };
        setSession(authUser);

        // New Google accounts (role "analyst", same as a normal
        // registration) go to plan selection rather than /upload.
        navigate("/select-plan", { replace: true });
      } catch {
        setError("Could not complete Google sign-in. Please try again.");
      }
    })();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div style={{
      minHeight: "100vh", display: "flex", alignItems: "center", justifyContent: "center",
      background: c.paper, fontFamily: FONT_BODY, color: c.ink,
    }}>
      <style>{FONT_IMPORT}</style>
      {error ? (
        <div style={{ display: "flex", flexDirection: "column", gap: "12px", maxWidth: "340px" }}>
          <span style={{ fontFamily: FONT_DISPLAY, fontSize: "18px", fontWeight: 700 }}>
            Sign-in failed
          </span>
          <span style={{ fontSize: "13px", color: c.flag }}>{error}</span>
          <Link to="/login" style={{ color: c.verified, fontSize: "13px", fontWeight: 600, textDecoration: "none" }}>
            Back to sign in
          </Link>
        </div>
      ) : (
        <div style={{ display: "flex", alignItems: "center", gap: "10px", fontSize: "13px", color: c.muted }}>
          <span style={{
            width: "14px", height: "14px", borderRadius: "50%",
            border: `2px solid ${c.line}`, borderTopColor: c.ink,
            animation: "spin .7s linear infinite", display: "inline-block",
          }} />
          Finishing sign-in…
          <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
        </div>
      )}
    </div>
  );
}
