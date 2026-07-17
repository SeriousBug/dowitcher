import { useState } from "react";
import {
  Copy,
  Check,
  HardDrive,
  KeyRound,
  Plus,
  RefreshCw,
  Trash2,
  ShieldCheck,
} from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { PageHeader } from "../components/PageHeader";
import { useAuth } from "../auth/AuthProvider";
import { useLiveData } from "../live/LiveData";
import { formatDate, formatRelative } from "../lib/format";
import type { Invite } from "../api/generated";

// TODO(Settings): wire invites to GET/POST /api/invites and
// DELETE /api/invites/{token}; passkey removal to DELETE /api/credentials/{id};
// and the rescan button to POST /api/library/rescan. Credentials and library
// status below are already live — they come from useAuth() and the WS.
const invites: Invite[] = [];

export function SettingsPage() {
  const { user, credentials } = useAuth();
  const { library } = useLiveData();
  const [confirmRemove, setConfirmRemove] = useState<{ id: string; name: string } | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  async function copyInvite(token: string) {
    const url = `${location.origin}/enroll?token=${token}`;
    try {
      await navigator.clipboard.writeText(url);
      setCopied(token);
      setTimeout(() => setCopied((c) => (c === token ? null : c)), 1800);
    } catch {
      // clipboard unavailable; ignore
    }
  }

  return (
    <div className={vstack({ gap: "8", alignItems: "stretch", maxW: "3xl" })}>
      <PageHeader eyebrow="Setup" title="Settings" subtitle={`Signed in as ${user?.name ?? ""}`} />

      <Section
        icon={<HardDrive size={17} className={css({ color: "textMuted" })} />}
        title="Library folder"
        action={
          <Button icon={<RefreshCw size={15} />} busy={library?.scanning}>
            {library?.scanning ? "Scanning…" : "Scan now"}
          </Button>
        }
      >
        <div className={vstack({ gap: "2", alignItems: "stretch" })}>
          <code
            className={css({
              px: "3",
              py: "2.5",
              borderRadius: "md",
              bg: "bg",
              borderWidth: "1px",
              borderColor: "border",
              fontFamily: "mono",
              fontSize: "sm",
              color: "ink.200",
              wordBreak: "break-all",
            })}
          >
            {library?.root ?? "…"}
          </code>
          <p className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.6" })}>
            Longbox watches this folder and picks up CBZ files as they appear.{" "}
            {library
              ? `${library.comicCount} found${library.lastScan ? `, last checked ${formatRelative(library.lastScan)}` : ""}.`
              : ""}
          </p>
        </div>
      </Section>

      <Section
        icon={<KeyRound size={17} className={css({ color: "textMuted" })} />}
        title="Your passkeys"
      >
        {credentials.length === 0 ? (
          <p className={css({ color: "textMuted", fontSize: "sm" })}>
            No passkeys on this account yet.
          </p>
        ) : (
          <div className={vstack({ gap: "2", alignItems: "stretch" })}>
            {credentials.map((cred) => (
              <div
                key={cred.id}
                className={hstack({
                  gap: "3",
                  justify: "space-between",
                  px: "3.5",
                  py: "3",
                  borderRadius: "md",
                  bg: "bg",
                  borderWidth: "1px",
                  borderColor: "border",
                })}
              >
                <div className={vstack({ gap: "0.5", alignItems: "flex-start", minW: "0" })}>
                  <span className={css({ fontSize: "sm", fontWeight: "semibold", truncate: true })}>
                    {cred.name}
                  </span>
                  <span className={css({ fontSize: "xs", color: "textMuted" })}>
                    Added {formatDate(cred.createdAt)}
                    {cred.lastUsed ? ` · last used ${formatRelative(cred.lastUsed)}` : ""}
                  </span>
                </div>
                <button
                  onClick={() => setConfirmRemove({ id: cred.id, name: cred.name })}
                  // Signing yourself out of your own account permanently is a
                  // real risk here, so the last passkey can't be removed.
                  disabled={credentials.length === 1}
                  aria-label={`Remove ${cred.name}`}
                  title={
                    credentials.length === 1
                      ? "This is your only passkey — add another before removing this one"
                      : "Remove this passkey"
                  }
                  className={css({
                    p: "2",
                    borderRadius: "md",
                    color: "textMuted",
                    cursor: "pointer",
                    flexShrink: 0,
                    _hover: { color: "danger", bg: "surfaceRaised" },
                    _disabled: { opacity: 0.35, cursor: "not-allowed", _hover: { color: "textMuted", bg: "transparent" } },
                  })}
                >
                  <Trash2 size={15} />
                </button>
              </div>
            ))}
          </div>
        )}
      </Section>

      {user?.isAdmin && (
        <Section
          icon={<ShieldCheck size={17} className={css({ color: "textMuted" })} />}
          title="Invites"
          action={
            <Button variant="primary" icon={<Plus size={15} />}>
              Create invite
            </Button>
          }
        >
          {invites.length === 0 ? (
            <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
              No invites waiting. Create one and send the link to whoever you want
              on this Longbox — they set up their own passkey, and the link works
              once.
            </p>
          ) : (
            <div className={vstack({ gap: "2", alignItems: "stretch" })}>
              {invites.map((invite) => (
                <div
                  key={invite.token}
                  className={hstack({
                    gap: "3",
                    justify: "space-between",
                    px: "3.5",
                    py: "3",
                    borderRadius: "md",
                    bg: "bg",
                    borderWidth: "1px",
                    borderColor: "border",
                  })}
                >
                  <div className={vstack({ gap: "0.5", alignItems: "flex-start", minW: "0" })}>
                    <span className={css({ fontSize: "sm", fontWeight: "semibold" })}>
                      {invite.forUserName
                        ? `New passkey for ${invite.forUserName}`
                        : invite.isAdmin
                          ? "New admin"
                          : "New reader"}
                    </span>
                    <span className={css({ fontSize: "xs", color: "textMuted" })}>
                      Expires {formatDate(invite.expiresAt)}
                    </span>
                  </div>
                  <div className={hstack({ gap: "1", flexShrink: 0 })}>
                    <Button
                      variant="ghost"
                      icon={copied === invite.token ? <Check size={15} /> : <Copy size={15} />}
                      onClick={() => copyInvite(invite.token)}
                    >
                      {copied === invite.token ? "Copied" : "Copy link"}
                    </Button>
                    <Button
                      variant="ghost"
                      icon={<Trash2 size={15} />}
                      aria-label="Revoke invite"
                      title="Revoke invite"
                    />
                  </div>
                </div>
              ))}
            </div>
          )}
        </Section>
      )}

      <ConfirmDialog
        open={confirmRemove !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmRemove(null);
        }}
        title="Remove this passkey?"
        description={
          <>
            <strong>{confirmRemove?.name}</strong> won't be able to sign in to
            Longbox any more. Your other passkeys keep working.
          </>
        }
        confirmLabel="Remove"
        tone="danger"
        onConfirm={() => {
          // TODO(Settings): DELETE /api/credentials/{confirmRemove.id}
        }}
      />
    </div>
  );
}

function Section({
  icon,
  title,
  action,
  children,
}: {
  icon: React.ReactNode;
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className={vstack({ gap: "4", alignItems: "stretch" })}>
      <div className={hstack({ gap: "3", justify: "space-between" })}>
        <h2 className={hstack({ gap: "2.5", fontSize: "sm", fontWeight: "bold", color: "text" })}>
          {icon}
          {title}
        </h2>
        {action}
      </div>
      <div
        className={css({
          p: "5",
          borderRadius: "lg",
          bg: "surface",
          borderWidth: "1px",
          borderColor: "border",
        })}
      >
        {children}
      </div>
    </section>
  );
}
