import { createContext, use, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { http, HttpError } from "../api/http";
import { clearOfflineData } from "../offline/downloads";
import { cacheSession, forgetCachedSession, readCachedSession } from "../offline/sessionCache";
import type { Session, User, Credential } from "../api/generated";

interface AuthContextValue {
  user: User | null;
  credentials: Credential[];
  loading: boolean;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

const meQueryKey = ["auth", "me"] as const;

// Logged out is an answer, not a failure: 401 becomes null so the query settles
// into a "no one is signed in" state instead of retrying and surfacing an error.
//
// An unreachable server is neither. It is the absence of an answer, and the two
// must not collapse together: treating "couldn't ask" as "signed out" is what
// sends an offline reader to a login page that cannot reach the server either,
// past the comics they downloaded for exactly this moment.
async function fetchSession(): Promise<Session | null> {
  try {
    const session = await http.get<Session>("/auth/me");
    await cacheSession(session);
    return session;
  } catch (err) {
    if (err instanceof HttpError && err.status === 401) {
      await forgetCachedSession();
      return null;
    }
    // Only a transport failure falls back. An HttpError means the server did
    // reach us, and a broken server that answers 500 should surface as broken
    // rather than resolve to a stale identity.
    if (err instanceof HttpError) throw err;
    const cached = await readCachedSession();
    if (cached) return cached;
    throw err;
  }
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: meQueryKey,
    queryFn: fetchSession,
    retry: false,
    staleTime: 30_000,
  });

  const session = query.data ?? null;

  const value: AuthContextValue = {
    user: session?.user ?? null,
    credentials: session?.credentials ?? [],
    loading: query.isLoading,
    refresh: async () => {
      await queryClient.invalidateQueries({ queryKey: meQueryKey });
    },
    logout: async () => {
      await http.post("/auth/logout");
      // Downloaded comics are readable with no server to ask, so the session
      // ending is the only thing that can take them away. Sign-out on a shared
      // or borrowed device has to leave nothing behind — that is the price of
      // offline reading being possible at all.
      //
      // Before the query cache is cleared, and awaited: a signed-out client
      // that still has pages on disk is the failure this exists to prevent.
      await clearOfflineData();
      queryClient.setQueryData(meQueryKey, null);
      await queryClient.invalidateQueries({ queryKey: meQueryKey });
    },
  };

  return <AuthContext value={value}>{children}</AuthContext>;
}

export function useAuth(): AuthContextValue {
  const ctx = use(AuthContext);
  if (!ctx) throw new Error("useAuth must be used within AuthProvider");
  return ctx;
}
