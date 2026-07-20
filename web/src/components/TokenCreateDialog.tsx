import { useEffect, useState } from "react";
import { Dialog, Portal } from "@ark-ui/react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, KeyRound } from "lucide-react";
import { css } from "styled-system/css";
import { hstack, vstack } from "styled-system/patterns";
import { http, HttpError } from "../api/http";
import { toaster } from "../lib/toaster";
import { Button } from "./Button";
import type { CreateTokenResponse } from "../api/generated";

/**
 * Mints an API token and reveals the secret exactly once. The server keeps only
 * a hash, so there is no second chance to read it — the dialog stays on the
 * secret until the user has copied it and closes deliberately, rather than
 * flashing it in a toast that scrolls away.
 */
export function TokenCreateDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  // Each open starts a fresh mint: an old secret left on screen from last time
  // is both confusing and a thing to leak.
  useEffect(() => {
    if (open) {
      setName("");
      setSecret(null);
      setCopied(false);
    }
  }, [open]);

  const create = useMutation({
    mutationFn: (n: string) => http.post<CreateTokenResponse>("/api/tokens", { name: n }),
    onSuccess: (res) => {
      queryClient.invalidateQueries({ queryKey: ["tokens"] });
      setSecret(res.secret);
    },
    onError: (err) =>
      toaster.create({
        type: "error",
        title: "Couldn't create a token",
        description:
          err instanceof HttpError ? err.message : "Something went wrong. Please try again.",
      }),
  });

  async function copySecret() {
    if (!secret) return;
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
      setTimeout(() => setCopied(false), 1800);
    } catch {
      toaster.create({
        type: "info",
        title: "Copy this token yourself",
        description: "Clipboard access is blocked here — select the token and copy it by hand.",
      });
    }
  }

  const mcpUrl = `${window.location.origin}/mcp`;

  return (
    <Dialog.Root open={open} onOpenChange={(d) => onOpenChange(d.open)} lazyMount unmountOnExit>
      <Portal>
        <Dialog.Backdrop
          className={css({
            position: "fixed",
            inset: "0",
            bg: "rgba(10, 8, 9, 0.7)",
            backdropFilter: "blur(3px)",
            zIndex: "50",
          })}
        />
        <Dialog.Positioner
          className={css({
            position: "fixed",
            inset: "0",
            display: "flex",
            alignItems: "center",
            justifyContent: "center",
            p: "4",
            zIndex: "50",
          })}
        >
          <Dialog.Content
            className={vstack({
              gap: "5",
              alignItems: "stretch",
              w: "full",
              maxW: "md",
              p: "6",
              bg: "surfaceRaised",
              borderWidth: "1px",
              borderColor: "border",
              borderRadius: "xl",
              boxShadow: "pop",
            })}
          >
            <div className={vstack({ gap: "1.5", alignItems: "stretch" })}>
              <Dialog.Title className={css({ fontSize: "xl", fontWeight: "bold", color: "text" })}>
                {secret ? "Copy your token now" : "New API token"}
              </Dialog.Title>
              <Dialog.Description className={css({ color: "textMuted", fontSize: "sm", lineHeight: "1.6" })}>
                {secret
                  ? "This is the only time it's shown. Store it somewhere safe — you can't read it again, only revoke it."
                  : "An API token lets an AI agent manage your library through the MCP server. It acts as you, and only ever sees what you can see."}
              </Dialog.Description>
            </div>

            {secret ? (
              <div className={vstack({ gap: "4", alignItems: "stretch" })}>
                <div className={vstack({ gap: "2", alignItems: "stretch" })}>
                  <span className={css({ fontSize: "xs", fontWeight: "semibold", color: "textMuted" })}>
                    Token
                  </span>
                  <code className={SECRET_BOX}>{secret}</code>
                </div>
                <div className={vstack({ gap: "2", alignItems: "stretch" })}>
                  <span className={css({ fontSize: "xs", fontWeight: "semibold", color: "textMuted" })}>
                    MCP server URL
                  </span>
                  <code className={SECRET_BOX}>{mcpUrl}</code>
                  <p className={css({ fontSize: "xs", color: "textMuted", lineHeight: "1.6" })}>
                    Point your agent at this URL and authenticate with the token as a bearer
                    credential. The MCP server has to be enabled on this instance for it to answer.
                  </p>
                </div>
                <div className={hstack({ gap: "3", justify: "flex-end" })}>
                  <Button
                    variant="primary"
                    icon={copied ? <Check size={16} /> : <Copy size={16} />}
                    onClick={copySecret}
                  >
                    {copied ? "Copied" : "Copy token"}
                  </Button>
                  <Dialog.CloseTrigger asChild>
                    <Button variant="ghost">Done</Button>
                  </Dialog.CloseTrigger>
                </div>
              </div>
            ) : (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const n = name.trim();
                  if (n) create.mutate(n);
                }}
                className={vstack({ gap: "4", alignItems: "stretch" })}
              >
                <label className={vstack({ gap: "2", alignItems: "stretch" })}>
                  <span className={css({ fontSize: "sm", fontWeight: "semibold", color: "text" })}>
                    Name
                  </span>
                  <div
                    className={hstack({
                      gap: "2.5",
                      px: "3.5",
                      py: "2.5",
                      borderRadius: "md",
                      bg: "bg",
                      borderWidth: "1px",
                      borderColor: "border",
                      _focusWithin: { borderColor: "accent" },
                    })}
                  >
                    <KeyRound size={15} className={css({ color: "ink.500", flexShrink: 0 })} />
                    <input
                      autoFocus
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      placeholder="e.g. Claude on my laptop"
                      className={css({
                        flex: "1",
                        minW: "0",
                        bg: "transparent",
                        color: "text",
                        fontSize: "sm",
                        _placeholder: { color: "ink.500" },
                        _focus: { outline: "none" },
                      })}
                    />
                  </div>
                  <span className={css({ fontSize: "xs", color: "textMuted" })}>
                    A label so you know which agent holds it.
                  </span>
                </label>
                <div className={hstack({ gap: "3", justify: "flex-end" })}>
                  <Dialog.CloseTrigger asChild>
                    <Button variant="ghost" type="button">
                      Never mind
                    </Button>
                  </Dialog.CloseTrigger>
                  <Button
                    variant="primary"
                    type="submit"
                    busy={create.isPending}
                    disabled={name.trim() === ""}
                    icon={<Check size={16} />}
                  >
                    Create token
                  </Button>
                </div>
              </form>
            )}
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  );
}

const SECRET_BOX = css({
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
  userSelect: "all",
});
