import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { startRegistration } from "@simplewebauthn/browser";
import type { PublicKeyCredentialCreationOptionsJSON } from "@simplewebauthn/browser";
import {
  Copy,
  Check,
  HardDrive,
  KeyRound,
  LifeBuoy,
  Plus,
  RefreshCw,
  Trash2,
  ShieldCheck,
  Users as UsersIcon,
} from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { Button } from "../components/Button";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { PageHeader } from "../components/PageHeader";
import { useAuth } from "../auth/AuthProvider";
import { useLiveData } from "../live/LiveData";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { formatDate, formatRelative } from "../lib/format";
import type { Invite, User } from "../api/generated";

interface CreationOptions {
  publicKey: PublicKeyCredentialCreationOptionsJSON;
}

/** Every mutation on this page fails the same way, so it's said once. */
function failed(title: string, fallback = "Something went wrong. Please try again.") {
  return (err: unknown) =>
    toaster.create({
      type: "error",
      title,
      description: err instanceof HttpError ? err.message : fallback,
    });
}

/**
 * Invite has no URL field on purpose — the server has no reliable idea what
 * address a reverse proxy is publishing it under, so the only honest origin is
 * the one the browser is already looking at.
 */
function inviteLink(token: string): string {
  return `${window.location.origin}/enroll?token=${token}`;
}

export function SettingsPage() {
  const { user, credentials, refresh } = useAuth();
  const { library } = useLiveData();
  const queryClient = useQueryClient();
  const [confirmRemove, setConfirmRemove] = useState<{ id: string; name: string } | null>(null);
  const [confirmDeleteUser, setConfirmDeleteUser] = useState<User | null>(null);
  const [copied, setCopied] = useState<string | null>(null);
  const [enrolling, setEnrolling] = useState(false);

  const invitesQuery = useQuery({
    queryKey: ["invites"],
    queryFn: () => http.get<Invite[]>("/api/invites"),
    enabled: Boolean(user?.isAdmin),
  });

  const usersQuery = useQuery({
    queryKey: ["users"],
    queryFn: () => http.get<User[]>("/api/users"),
    enabled: Boolean(user?.isAdmin),
  });

  async function copyInvite(token: string) {
    try {
      await navigator.clipboard.writeText(inviteLink(token));
      setCopied(token);
      setTimeout(() => setCopied((c) => (c === token ? null : c)), 1800);
    } catch {
      // Clipboard access is denied outside a secure context, which is exactly
      // where a self-hosted instance often lives. Show the link so it can be
      // copied by hand rather than failing silently.
      toaster.create({
        type: "info",
        title: "Copy this link yourself",
        description: inviteLink(token),
      });
    }
  }

  const scan = useMutation({
    mutationFn: () => http.post<{ ok: boolean }>("/api/library/scan"),
    onSuccess: () => {
      // Progress arrives over the stream; there is nothing to invalidate.
      toaster.create({ type: "success", title: "Reading your shelves now" });
    },
    onError: failed("Couldn't start a scan"),
  });

  const removeCredential = useMutation({
    mutationFn: (id: string) => http.del<{ ok: boolean }>(`/auth/credentials/${id}`),
    onSuccess: async () => {
      await refresh();
      toaster.create({ type: "success", title: "Passkey removed" });
    },
    onError: failed("Couldn't remove that passkey"),
  });

  async function addPasskey() {
    setEnrolling(true);
    try {
      const options = await http.post<CreationOptions>("/auth/register/device/begin");
      const credential = await startRegistration({ optionsJSON: options.publicKey });
      await http.post("/auth/register/device/finish", credential as unknown as Record<string, unknown>);
      await refresh();
      toaster.create({ type: "success", title: "Passkey added" });
    } catch (err) {
      // Backing out of the system passkey sheet is a choice, not an error.
      if (err instanceof DOMException && err.name === "NotAllowedError") {
        toaster.create({ type: "info", title: "That was cancelled" });
      } else {
        failed("Couldn't add that passkey")(err);
      }
    } finally {
      setEnrolling(false);
    }
  }

  const createInvite = useMutation({
    mutationFn: (isAdmin: boolean) => http.post<Invite>("/api/invites", { isAdmin }),
    onSuccess: (invite) => {
      queryClient.invalidateQueries({ queryKey: ["invites"] });
      copyInvite(invite.token);
      toaster.create({
        type: "success",
        title: "Invite ready",
        description: "The link is on your clipboard. It works once.",
      });
    },
    onError: failed("Couldn't create an invite"),
  });

  const revokeInvite = useMutation({
    mutationFn: (token: string) => http.del<{ ok: boolean }>(`/api/invites/${token}`),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["invites"] });
      toaster.create({ type: "success", title: "Invite revoked" });
    },
    onError: failed("Couldn't revoke that invite"),
  });

  const resetUser = useMutation({
    mutationFn: (target: User) => http.post<Invite>(`/api/users/${target.id}/reset`, {}),
    onSuccess: (invite, target) => {
      queryClient.invalidateQueries({ queryKey: ["invites"] });
      copyInvite(invite.token);
      toaster.create({
        type: "success",
        title: `Recovery link for ${target.name}`,
        description: "It's on your clipboard. Sending it lets them enrol a new passkey.",
      });
    },
    onError: failed("Couldn't make a recovery link"),
  });

  const deleteUser = useMutation({
    mutationFn: (target: User) => http.del<{ ok: boolean }>(`/api/users/${target.id}`),
    onSuccess: (_data, target) => {
      queryClient.invalidateQueries({ queryKey: ["users"] });
      toaster.create({ type: "success", title: `Removed ${target.name}` });
    },
    onError: failed("Couldn't remove that person"),
  });

  const invites = invitesQuery.data ?? [];
  const users = usersQuery.data ?? [];

  return (
    <div className={vstack({ gap: "8", alignItems: "stretch", maxW: "3xl" })}>
      <PageHeader eyebrow="Setup" title="Settings" subtitle={`Signed in as ${user?.name ?? ""}`} />

      <Section
        icon={<HardDrive size={17} className={css({ color: "textMuted" })} />}
        title="Library folder"
        action={
          user?.isAdmin ? (
            <Button
              icon={<RefreshCw size={15} />}
              busy={library?.scanning || scan.isPending}
              onClick={() => scan.mutate()}
            >
              {library?.scanning ? "Scanning…" : "Scan now"}
            </Button>
          ) : undefined
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
            Dowitcher watches this folder and picks up CBZ files as they appear.{" "}
            {library
              ? `${library.comicCount} found${library.lastScan ? `, last checked ${formatRelative(library.lastScan)}` : ""}.`
              : ""}
          </p>
        </div>
      </Section>

      <Section
        icon={<KeyRound size={17} className={css({ color: "textMuted" })} />}
        title="Your passkeys"
        action={
          <Button icon={<Plus size={15} />} busy={enrolling} onClick={addPasskey}>
            Add a passkey
          </Button>
        }
      >
        {credentials.length === 0 ? (
          <p className={css({ color: "textMuted", fontSize: "sm" })}>
            No passkeys on this account yet.
          </p>
        ) : (
          <div className={vstack({ gap: "2", alignItems: "stretch" })}>
            {credentials.map((cred) => (
              <Row
                key={cred.id}
                title={cred.name}
                subtitle={`Added ${formatDate(cred.createdAt)}${
                  cred.lastUsed ? ` · last used ${formatRelative(cred.lastUsed)}` : ""
                }`}
              >
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
                  className={ICON_BUTTON}
                >
                  <Trash2 size={15} />
                </button>
              </Row>
            ))}
          </div>
        )}
      </Section>

      {user?.isAdmin && (
        <Section
          icon={<ShieldCheck size={17} className={css({ color: "textMuted" })} />}
          title="Invites"
          action={
            <Button
              variant="primary"
              icon={<Plus size={15} />}
              busy={createInvite.isPending}
              onClick={() => createInvite.mutate(false)}
            >
              Create invite
            </Button>
          }
        >
          {invitesQuery.isLoading ? (
            <p className={css({ color: "textMuted", fontSize: "sm" })}>Looking…</p>
          ) : invites.length === 0 ? (
            <p className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
              No invites waiting. Create one and send the link to whoever you want
              on this Dowitcher — they set up their own passkey, and the link works
              once.
            </p>
          ) : (
            <div className={vstack({ gap: "2", alignItems: "stretch" })}>
              {invites.map((invite) => (
                <Row
                  key={invite.token}
                  title={
                    invite.forUserName
                      ? `New passkey for ${invite.forUserName}`
                      : invite.isAdmin
                        ? "New admin"
                        : "New reader"
                  }
                  subtitle={`Expires ${formatDate(invite.expiresAt)}`}
                >
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
                      onClick={() => revokeInvite.mutate(invite.token)}
                    />
                  </div>
                </Row>
              ))}
            </div>
          )}
        </Section>
      )}

      {user?.isAdmin && (
        <Section
          icon={<UsersIcon size={17} className={css({ color: "textMuted" })} />}
          title="Everyone here"
        >
          {usersQuery.isLoading ? (
            <p className={css({ color: "textMuted", fontSize: "sm" })}>Looking…</p>
          ) : users.length === 0 ? (
            <p className={css({ color: "textMuted", fontSize: "sm" })}>
              No one has enrolled yet.
            </p>
          ) : (
            <div className={vstack({ gap: "2", alignItems: "stretch" })}>
              {users.map((person) => (
                <Row
                  key={person.id}
                  title={person.name}
                  badge={person.isAdmin ? "Admin" : undefined}
                  subtitle={`Joined ${formatDate(person.createdAt)}${
                    person.id === user.id ? " · that's you" : ""
                  }`}
                >
                  <div className={hstack({ gap: "1", flexShrink: 0 })}>
                    <Button
                      variant="ghost"
                      icon={<LifeBuoy size={15} />}
                      aria-label={`Make a recovery link for ${person.name}`}
                      title="Lost their passkey? This mints a one-time link to enrol a new one."
                      busy={resetUser.isPending && resetUser.variables?.id === person.id}
                      onClick={() => resetUser.mutate(person)}
                    />
                    <button
                      onClick={() => setConfirmDeleteUser(person)}
                      // Deleting yourself is a locked door with the key inside.
                      disabled={person.id === user.id}
                      aria-label={`Remove ${person.name}`}
                      title={
                        person.id === user.id
                          ? "You can't remove your own account"
                          : `Remove ${person.name}`
                      }
                      className={ICON_BUTTON}
                    >
                      <Trash2 size={15} />
                    </button>
                  </div>
                </Row>
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
            Dowitcher any more. Your other passkeys keep working.
          </>
        }
        confirmLabel="Remove"
        tone="danger"
        onConfirm={() => confirmRemove && removeCredential.mutate(confirmRemove.id)}
      />

      <ConfirmDialog
        open={confirmDeleteUser !== null}
        onOpenChange={(open) => {
          if (!open) setConfirmDeleteUser(null);
        }}
        title="Remove this person?"
        description={
          <>
            <strong>{confirmDeleteUser?.name}</strong> loses access to this
            Dowitcher, along with their passkeys, reading progress and anything they
            uploaded. This can't be undone.
          </>
        }
        confirmLabel="Remove"
        tone="danger"
        onConfirm={() => confirmDeleteUser && deleteUser.mutate(confirmDeleteUser)}
      />
    </div>
  );
}

const ICON_BUTTON = css({
  p: "2",
  borderRadius: "md",
  color: "textMuted",
  cursor: "pointer",
  flexShrink: 0,
  _hover: { color: "danger", bg: "surfaceRaised" },
  _disabled: {
    opacity: 0.35,
    cursor: "not-allowed",
    _hover: { color: "textMuted", bg: "transparent" },
  },
});

function Row({
  title,
  subtitle,
  badge,
  children,
}: {
  title: string;
  subtitle: string;
  badge?: string;
  children: React.ReactNode;
}) {
  return (
    <div
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
        <span className={hstack({ gap: "2", minW: "0" })}>
          <span className={css({ fontSize: "sm", fontWeight: "semibold", truncate: true })}>
            {title}
          </span>
          {badge && (
            <span
              className={css({
                px: "1.5",
                py: "0.5",
                borderRadius: "sm",
                bg: "accentQuiet",
                color: "magenta.300",
                fontSize: "2xs",
                fontWeight: "bold",
                flexShrink: 0,
              })}
            >
              {badge}
            </span>
          )}
        </span>
        <span className={css({ fontSize: "xs", color: "textMuted" })}>{subtitle}</span>
      </div>
      {children}
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
