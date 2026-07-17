import { useState, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { startRegistration } from "@simplewebauthn/browser";
import type { PublicKeyCredentialCreationOptionsJSON } from "@simplewebauthn/browser";
import { Loader2, Wand2 } from "lucide-react";
import { css } from "styled-system/css";
import { vstack } from "styled-system/patterns";
import { http, HttpError } from "../api/http";
import { useAuth } from "../auth/AuthProvider";
import { AuthCard, ErrorBanner, SpineMark } from "./Login";

interface CreationOptions {
  publicKey: PublicKeyCredentialCreationOptionsJSON;
}

export function Enroll({ token }: { token: string }) {
  const navigate = useNavigate();
  const { refresh } = useAuth();
  const [name, setName] = useState("My passkey");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!token) {
    return (
      <AuthCard>
        <Header title="This link is missing its code">
          An invite link carries a code that this one doesn't have. Ask whoever
          runs this Dowitcher to send you a fresh one.
        </Header>
      </AuthCard>
    );
  }

  async function createPasskey() {
    setBusy(true);
    setError(null);
    try {
      const options = await http.post<CreationOptions>("/auth/register/begin", {
        token,
        name: name.trim() || "My passkey",
      });
      const credential = await startRegistration({ optionsJSON: options.publicKey });
      await http.post("/auth/register/finish", credential as unknown as Record<string, unknown>);
      await refresh();
      await navigate({ to: "/" });
    } catch (err) {
      // Backing out of the system passkey sheet is a choice, not an error.
      if (err instanceof DOMException && err.name === "NotAllowedError") {
        setError("That was cancelled. Try again whenever you're ready.");
      } else if (err instanceof HttpError && err.status >= 400 && err.status < 500) {
        setError("This invite has already been used or has expired. Ask for a new one.");
      } else {
        setError("Something went wrong setting up your passkey. Please try again.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthCard>
      <SpineMark />

      <Header title="Set up your passkey">
        Name this device so you can tell it apart later, then create a passkey.
        That's your way in from now on.
      </Header>

      <label className={vstack({ gap: "1.5", alignItems: "stretch", w: "full" })}>
        <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
          Passkey name
        </span>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="My passkey"
          disabled={busy}
          className={css({
            w: "full",
            px: "3.5",
            py: "2.5",
            borderRadius: "md",
            borderWidth: "1px",
            borderColor: "border",
            bg: "bg",
            color: "text",
            fontSize: "sm",
            _placeholder: { color: "ink.500" },
            _focus: { outline: "none", borderColor: "accent" },
          })}
        />
      </label>

      <button
        onClick={createPasskey}
        disabled={busy}
        className={css({
          display: "flex",
          alignItems: "center",
          justifyContent: "center",
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
          <Loader2 size={19} className={css({ animation: "spin 0.9s linear infinite" })} />
        ) : (
          <Wand2 size={19} />
        )}
        {busy ? "Creating your passkey…" : "Create passkey"}
      </button>

      {error && <ErrorBanner>{error}</ErrorBanner>}
    </AuthCard>
  );
}

function Header({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className={vstack({ gap: "2", textAlign: "center" })}>
      <h1 className={css({ fontSize: "2xl", fontWeight: "bold", letterSpacing: "-0.03em" })}>
        {title}
      </h1>
      <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>{children}</p>
    </div>
  );
}
