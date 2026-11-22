// frontend/src/context/AuthContext.tsx
//
// login() now supports the two step-up challenges routers/auth.go's
// handleLogin can return: captcha_required (after captchaFailThreshold
// failed attempts) and otp_required (after otpFailThreshold). Both throw
// a typed LoginChallengeError instead of a generic Error, so the login
// form can react (show the CAPTCHA widget, show an OTP input) instead of
// just displaying a failure message.
import { createContext, useContext, useState, useCallback, ReactNode } from "react"

export type UserRole = "analyst" | "reviewer" | "admin" | "debug"

export interface AuthUser {
  user_id:      string
  username:     string
  email:        string
  role:         UserRole
  access_token: string
}

export interface LoginChallenge {
  captchaRequired?: boolean
  otpRequired?:     boolean
}

// Thrown instead of a plain Error when the server needs a CAPTCHA token
// or an OTP code before it will issue an access token. err.message is
// still safe to display as-is; err.challenge tells the caller which
// extra step to show.
export class LoginChallengeError extends Error {
  challenge: LoginChallenge
  constructor(message: string, challenge: LoginChallenge) {
    super(message)
    this.name = "LoginChallengeError"
    this.challenge = challenge
  }
}

export interface LoginOptions {
  captchaToken?: string
  otpCode?:      string
}

interface AuthContextValue {
  user:       AuthUser | null
  login:      (login: string, password: string, opts?: LoginOptions) => Promise<AuthUser>
  register:   (username: string, email: string, password: string, role: UserRole) => Promise<void>
  logout:     () => void
  isAuthed:   boolean
  // setSession accepts an AuthUser obtained OUTSIDE the normal login()
  // flow -- specifically for OAuthCallbackPage.tsx, which gets a user
  // object back from /auth/session after the Google OAuth handshake
  // rather than calling /api/auth/login itself. Goes through the same
  // _persist path as login/register, so it's stored under the same
  // hitl_auth key everything else reads -- OAuthCallbackPage previously
  // wrote to separate "authToken"/"authUser" keys that AuthContext never
  // read, meaning the app still saw the user as logged out afterward.
  setSession: (user: AuthUser) => void
}

const AuthContext = createContext<AuthContextValue | null>(null)
const TOKEN_KEY   = "hitl_auth"

function loadStoredUser(): AuthUser | null {
  try {
    const raw = localStorage.getItem(TOKEN_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(loadStoredUser)

  const _persist = (u: AuthUser) => {
    localStorage.setItem(TOKEN_KEY, JSON.stringify(u))
    setUser(u)
  }

  const login = useCallback(async (
    loginVal: string, password: string, opts?: LoginOptions
  ): Promise<AuthUser> => {
    const res = await fetch("/api/auth/login", {
      method:  "POST",
      headers: { "Content-Type": "application/json" },
      body:    JSON.stringify({
        login:         loginVal,
        password,
        captcha_token: opts?.captchaToken,
        otp_code:      opts?.otpCode,
      }),
    })
    const data = await res.json().catch(() => ({}))

    // 200 OK but no token yet -- the "we just sent you a code" case.
    // NOT a successful login; the caller must resubmit with otp_code.
    if (res.ok && data.otp_required) {
      throw new LoginChallengeError(
        data.message || "We sent a verification code to your email",
        { otpRequired: true },
      )
    }

    if (!res.ok) {
      throw new LoginChallengeError(
        data.detail || "Login failed",
        { captchaRequired: !!data.captcha_required, otpRequired: !!data.otp_required },
      )
    }

    const authUser: AuthUser = {
      user_id:      data.user_id,
      username:     data.username,
      email:        data.email,
      role:         data.role as UserRole,
      access_token: data.access_token,
    }
    _persist(authUser)
    return authUser
  }, [])

  const register = useCallback(async (
    username: string, email: string, password: string, role: UserRole
  ) => {
    const res = await fetch("/api/auth/register", {
      method:  "POST",
      headers: { "Content-Type": "application/json" },
      body:    JSON.stringify({ username, email, password, role }),
    })
    if (!res.ok) {
      const err = await res.json().catch(() => ({ detail: "Registration failed" }))
      throw new Error(err.detail || "Registration failed")
    }
    const data = await res.json()
    _persist({
      user_id:      data.user_id,
      username:     data.username,
      email:        data.email,
      role:         data.role as UserRole,
      access_token: data.access_token,
    })
  }, [])

  const setSession = useCallback((authUser: AuthUser) => {
    _persist(authUser)
  }, [])

  const logout = useCallback(async () => {
    const stored = localStorage.getItem(TOKEN_KEY)
    const token  = stored ? JSON.parse(stored)?.access_token : null
    if (token) {
      fetch("/api/auth/logout", {
        method:  "POST",
        headers: { Authorization: `Bearer ${token}` },
      }).catch(() => {})
    }
    localStorage.removeItem(TOKEN_KEY)
    setUser(null)
  }, [])

  return (
    <AuthContext.Provider value={{ user, login, register, logout, setSession, isAuthed: !!user }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>")
  return ctx
}

export function useAuthHeaders(): HeadersInit {
  const { user } = useAuth()
  return user ? { Authorization: `Bearer ${user.access_token}` } : {}
}
