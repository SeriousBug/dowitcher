import { useState, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { startAuthentication } from "@simplewebauthn/browser";
import type { PublicKeyCredentialRequestOptionsJSON } from "@simplewebauthn/browser";
import { Fingerprint, KeyRound, Loader2 } from "lucide-react";
import { css, cx } from "styled-system/css";
import { flex, hstack, vstack } from "styled-system/patterns";
import { http, HttpError } from "../api/http";
import { useAuth } from "../auth/AuthProvider";

interface RequestOptions {
  publicKey: PublicKeyCredentialRequestOptionsJSON;
}

export function Login() {
  const navigate = useNavigate();
  const { refresh } = useAuth();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function signIn() {
    setBusy(true);
    setError(null);
    try {
      const options = await http.post<RequestOptions>("/auth/login/begin");
      const credential = await startAuthentication({ optionsJSON: options.publicKey });
      await http.post("/auth/login/finish", credential as unknown as Record<string, unknown>);
      await refresh();
      await navigate({ to: "/" });
    } catch (err) {
      // Dismissing the system passkey sheet throws NotAllowedError. That is a
      // person changing their mind, not a failure, and it must not be dressed up
      // as one.
      if (err instanceof DOMException && err.name === "NotAllowedError") {
        setError("That was cancelled. Try again whenever you're ready.");
      } else if (err instanceof HttpError && err.status >= 400 && err.status < 500) {
        setError("No passkey here matches. Ask an admin for an invite link.");
      } else {
        setError("Something went wrong signing in. Please try again.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthCard>
      <SpineMark />

      <div className={vstack({ gap: "2", textAlign: "center" })}>
        <h1
          className={css({
            fontSize: "3xl",
            fontWeight: "bold",
            letterSpacing: "-0.03em",
          })}
        >
          Dowitcher
        </h1>
        <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
          Your comics, on your own shelf. Sign in with your passkey — there's no
          password to forget.
        </p>
      </div>

      <button
        onClick={signIn}
        disabled={busy}
        className={flex({
          align: "center",
          justify: "center",
          gap: "2.5",
          w: "full",
          px: "6",
          py: "3.5",
          borderRadius: "md",
          bg: "accent",
          color: "white",
          fontSize: "md",
          fontWeight: "bold",
          cursor: "pointer",
          transition: "background 0.15s ease",
          _hover: { bg: "accentHover" },
          _disabled: { opacity: 0.55, cursor: "not-allowed" },
        })}
      >
        {busy ? (
          <Loader2
            size={19}
            className={css({
              animation: "spin 0.9s linear infinite",
              _motionReduce: { animation: "none" },
            })}
          />
        ) : (
          <Fingerprint size={19} />
        )}
        {busy ? "Signing you in…" : "Sign in with a passkey"}
      </button>

      {error && <ErrorBanner>{error}</ErrorBanner>}
    </AuthCard>
  );
}

/**
 * The wordmark's row of spines, blown up for the one screen with nothing else on
 * it. Written out rather than mapped: Panda extracts styles statically, so a
 * token fed in from a variable produces no CSS at all.
 */
export function SpineMark() {
  const spine = css({ w: "2.5", borderRadius: "sm" });
  return (
    <span className={hstack({ gap: "1.5", alignItems: "flex-end", h: "14" })} aria-hidden>
      <span className={cx(spine, css({ h: "7", bg: "ink.700" }))} />
      <span className={cx(spine, css({ h: "10", bg: "ink.600" }))} />
      <span className={cx(spine, css({ h: "14", bg: "accent" }))} />
      <span className={cx(spine, css({ h: "11", bg: "ink.600" }))} />
      <span className={cx(spine, css({ h: "8", bg: "ink.700" }))} />
    </span>
  );
}

export function AuthCard({ children }: { children: ReactNode }) {
  return (
    <div className={flex({ align: "center", justify: "center", minH: "100vh", bg: "bg", p: "4" })}>
      <div
        className={vstack({
          gap: "6",
          alignItems: "center",
          w: "full",
          maxW: "sm",
          p: { base: "6", md: "9" },
          borderRadius: "xl",
          bg: "surface",
          borderWidth: "1px",
          borderColor: "border",
          boxShadow: "pop",
        })}
      >
        {children}
      </div>
    </div>
  );
}

export function ErrorBanner({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className={flex({
        align: "center",
        gap: "2.5",
        w: "full",
        px: "4",
        py: "3",
        borderRadius: "md",
        bg: "rgba(239, 75, 75, 0.1)",
        borderWidth: "1px",
        borderColor: "rust.700",
        color: "rust.300",
        fontWeight: "medium",
        fontSize: "sm",
        lineHeight: "1.5",
      })}
    >
      <KeyRound size={17} className={css({ flexShrink: 0, color: "danger" })} />
      {children}
    </div>
  );
}
