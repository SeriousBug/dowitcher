import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  DownloadCloud,
  FolderOpen,
  Library,
  Loader2,
  LogOut,
  Settings as SettingsIcon,
  Tags as TagsIcon,
  Upload,
} from "lucide-react";
import { css } from "styled-system/css";
import { flex, hstack, vstack } from "styled-system/patterns";
import { useAuth } from "../auth/AuthProvider";
import { LiveDataProvider, useLiveData } from "../live/LiveData";
import { ConnectionLight, ConnectionNotice } from "./ConnectionLight";
import { ToasterView } from "./ToasterView";

const NAV = [
  { to: "/", label: "Library", icon: Library },
  { to: "/collections", label: "Collections", icon: FolderOpen },
  { to: "/tags", label: "Tags", icon: TagsIcon },
  { to: "/downloads", label: "Offline", icon: DownloadCloud },
  { to: "/import", label: "Import", icon: Upload },
  { to: "/settings", label: "Settings", icon: SettingsIcon },
] as const;

const SIDEBAR_W = "228px";

function Wordmark() {
  return (
    <Link
      to="/"
      className={hstack({ gap: "2.5", textDecoration: "none", flexShrink: 0 })}
      aria-label="Dowitcher home"
    >
      {/* The wordmark is the object the app is named after, seen end-on: a row of
          spines standing in a box, one of them pulled proud of the rest. */}
      <span className={hstack({ gap: "2px", alignItems: "flex-end", h: "5" })} aria-hidden>
        <span className={css({ w: "2px", h: "3.5", bg: "ink.500", borderRadius: "1px" })} />
        <span className={css({ w: "2px", h: "5", bg: "accent", borderRadius: "1px" })} />
        <span className={css({ w: "2px", h: "4", bg: "ink.500", borderRadius: "1px" })} />
        <span className={css({ w: "2px", h: "3", bg: "ink.600", borderRadius: "1px" })} />
      </span>
      <span
        className={css({
          fontFamily: "heading",
          fontSize: "lg",
          fontWeight: "bold",
          letterSpacing: "-0.03em",
          color: "text",
        })}
      >
        Dowitcher
      </span>
    </Link>
  );
}

/** Divider-tab nav item: the active section grows a magenta tab on its edge. */
function NavItem({
  to,
  label,
  icon: Icon,
}: {
  to: string;
  label: string;
  icon: typeof Library;
}) {
  return (
    <Link
      to={to}
      activeOptions={{ exact: to === "/" }}
      className={hstack({
        position: "relative",
        gap: "3",
        px: "3",
        py: "2.5",
        borderRadius: "md",
        fontSize: "sm",
        fontWeight: "semibold",
        color: "textMuted",
        textDecoration: "none",
        transition: "background 0.15s ease, color 0.15s ease",
        _hover: { bg: "surface", color: "text" },
        "&[data-status='active']": {
          bg: "accentQuiet",
          color: "text",
        },
        "&[data-status='active']::before": {
          content: '""',
          position: "absolute",
          left: "0",
          top: "50%",
          transform: "translateY(-50%)",
          w: "3px",
          h: "5",
          borderRadius: "full",
          bg: "accent",
        },
      })}
    >
      <Icon size={17} strokeWidth={2} />
      {label}
    </Link>
  );
}

/** Live scan readout. Only present while there is something to report. */
function ScanStatus() {
  const { library } = useLiveData();
  if (!library) return null;

  if (library.scanning) {
    const pct = library.total > 0 ? Math.round((library.done / library.total) * 100) : 0;
    return (
      <div
        className={vstack({
          gap: "2",
          alignItems: "stretch",
          p: "3",
          borderRadius: "md",
          bg: "surface",
          borderWidth: "1px",
          borderColor: "border",
        })}
      >
        <span className={hstack({ gap: "2", fontSize: "xs", fontWeight: "semibold" })}>
          <Loader2 size={13} className={css({ animation: "spin 0.9s linear infinite", color: "accent" })} />
          Reading your shelves…
        </span>
        <span className={css({ h: "3px", borderRadius: "full", bg: "ink.750", overflow: "hidden" })}>
          <span
            className={css({ display: "block", h: "full", bg: "accent", transition: "width 0.3s ease" })}
            style={{ width: `${pct}%` }}
          />
        </span>
        <span className={css({ fontSize: "2xs", color: "textMuted" })}>
          {library.done} of {library.total || "?"} files
        </span>
      </div>
    );
  }

  return (
    <span className={css({ fontSize: "xs", color: "textMuted", px: "1" })}>
      {library.comicCount === 1 ? "1 comic" : `${library.comicCount} comics`} on the shelf
    </span>
  );
}

function UserStrip({
  compact = false,
}: {
  compact?: boolean;
}) {
  const { user, logout } = useAuth();
  const navigate = useNavigate();
  const [loggingOut, setLoggingOut] = useState(false);

  if (!user) return null;

  async function handleLogout() {
    setLoggingOut(true);
    try {
      await logout();
      await navigate({ to: "/login" });
    } finally {
      setLoggingOut(false);
    }
  }

  return (
    <div className={hstack({ gap: "2", minW: "0" })}>
      <span
        className={flex({
          align: "center",
          justify: "center",
          w: "8",
          h: "8",
          borderRadius: "full",
          bg: "surfaceRaised",
          borderWidth: "1px",
          borderColor: "border",
          color: "text",
          fontSize: "xs",
          fontWeight: "bold",
          flexShrink: 0,
        })}
      >
        {user.name.charAt(0).toUpperCase()}
      </span>
      {!compact && (
        <span
          className={css({
            fontSize: "sm",
            fontWeight: "semibold",
            color: "text",
            truncate: true,
            minW: "0",
            flex: "1",
          })}
        >
          {user.name}
        </span>
      )}
      <button
        onClick={handleLogout}
        disabled={loggingOut}
        title="Sign out"
        aria-label="Sign out"
        className={flex({
          align: "center",
          justify: "center",
          w: "8",
          h: "8",
          borderRadius: "md",
          color: "textMuted",
          cursor: "pointer",
          flexShrink: 0,
          transition: "background 0.15s ease, color 0.15s ease",
          _hover: { bg: "surfaceRaised", color: "danger" },
          _disabled: { opacity: 0.55, cursor: "not-allowed" },
        })}
      >
        {loggingOut ? (
          <Loader2 size={16} className={css({ animation: "spin 0.9s linear infinite" })} />
        ) : (
          <LogOut size={16} />
        )}
      </button>
    </div>
  );
}

/**
 * Chrome that stays out of the way: a quiet rail on desktop, a thumb-reachable
 * tab bar on a phone, and the covers doing all the talking in between.
 */
export function AppShell({ children }: { children: ReactNode }) {
  return (
    <LiveDataProvider>
      <div className={css({ minH: "100vh", bg: "bg" })}>
        <aside
          className={vstack({
            display: { base: "none", md: "flex" },
            position: "fixed",
            left: "0",
            top: "0",
            bottom: "0",
            w: SIDEBAR_W,
            gap: "6",
            alignItems: "stretch",
            px: "4",
            py: "5",
            borderRightWidth: "1px",
            borderColor: "border",
            bg: "bg",
            zIndex: "20",
          })}
        >
          <div className={hstack({ justify: "space-between", gap: "2", px: "1" })}>
            <Wordmark />
            <ConnectionLight />
          </div>

          <nav className={vstack({ gap: "1", alignItems: "stretch" })}>
            {NAV.map((item) => (
              <NavItem key={item.to} {...item} />
            ))}
          </nav>

          <div className={vstack({ gap: "3", alignItems: "stretch", mt: "auto" })}>
            <ConnectionNotice />
            <ScanStatus />
            <div className={css({ h: "1px", bg: "border" })} />
            <UserStrip />
          </div>
        </aside>

        <header
          className={hstack({
            display: { base: "flex", md: "none" },
            justify: "space-between",
            position: "sticky",
            top: "0",
            zIndex: "20",
            px: "4",
            h: "14",
            bg: "bg",
            borderBottomWidth: "1px",
            borderColor: "border",
          })}
        >
          <Wordmark />
          <div className={hstack({ gap: "3" })}>
            <ConnectionLight />
            <UserStrip compact />
          </div>
        </header>

        <main
          className={css({
            ml: { base: "0", md: SIDEBAR_W },
            px: { base: "4", md: "8" },
            py: { base: "6", md: "9" },
            // Room for the mobile tab bar to float over without covering the
            // last row of covers.
            pb: { base: "24", md: "9" },
            maxW: "7xl",
          })}
        >
          {children}
        </main>

        <nav
          className={hstack({
            display: { base: "flex", md: "none" },
            justify: "space-around",
            position: "fixed",
            left: "0",
            right: "0",
            bottom: "0",
            zIndex: "20",
            py: "2",
            // The first and last tabs otherwise sit under the rounded corners of
            // an iOS home-screen install.
            pl: "calc(token(spacing.3) + env(safe-area-inset-left))",
            pr: "calc(token(spacing.3) + env(safe-area-inset-right))",
            bg: "rgba(19, 16, 17, 0.92)",
            backdropFilter: "blur(10px)",
            borderTopWidth: "1px",
            borderColor: "border",
          })}
        >
          {NAV.map(({ to, label, icon: Icon }) => (
            <Link
              key={to}
              to={to}
              activeOptions={{ exact: to === "/" }}
              aria-label={label}
              className={vstack({
                gap: "1",
                flex: "1",
                py: "1.5",
                fontSize: "2xs",
                fontWeight: "semibold",
                color: "textMuted",
                textDecoration: "none",
                "&[data-status='active']": { color: "accent" },
              })}
            >
              <Icon size={19} strokeWidth={2} />
              {label}
            </Link>
          ))}
        </nav>

        <ToasterView />
      </div>
    </LiveDataProvider>
  );
}
