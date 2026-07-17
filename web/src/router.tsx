import { createRootRoute, createRoute, createRouter, Outlet } from "@tanstack/react-router";
import type { ComponentType } from "react";
import { AppShell } from "./components/AppShell";
import { AuthProvider } from "./auth/AuthProvider";
import { RequireAuth } from "./auth/RequireAuth";
import { CollectionDetailPage } from "./routes/CollectionDetail";
import { CollectionsPage } from "./routes/Collections";
import { Enroll } from "./routes/Enroll";
import { ImportPage } from "./routes/Import";
import { LibraryPage } from "./routes/Library";
import { Login } from "./routes/Login";
import { ReaderPage } from "./routes/Reader";
import { SettingsPage } from "./routes/Settings";
import { TagsPage } from "./routes/Tags";

/** Sign-in gate plus the nav shell — what every page inside the app wants. */
function protectedPage<P extends object>(Page: ComponentType<P>) {
  return function Guarded(props: P) {
    return (
      <RequireAuth>
        <AppShell>
          <Page {...props} />
        </AppShell>
      </RequireAuth>
    );
  };
}

/**
 * The reader is the exception: gated, but with no shell around it. A sidebar
 * beside a comic page is a sidebar you are reading past.
 */
function protectedFullscreen<P extends object>(Page: ComponentType<P>) {
  return function Guarded(props: P) {
    return (
      <RequireAuth>
        <Page {...props} />
      </RequireAuth>
    );
  };
}

const rootRoute = createRootRoute({
  component: () => (
    <AuthProvider>
      <Outlet />
    </AuthProvider>
  ),
});

const ProtectedLibrary = protectedPage(LibraryPage);
const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  component: ProtectedLibrary,
});

const ProtectedReader = protectedFullscreen(ReaderPage);
const comicRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/comic/$id",
  component: function ComicRoute() {
    const { id } = comicRoute.useParams();
    return <ProtectedReader id={id} />;
  },
});

const ProtectedCollections = protectedPage(CollectionsPage);
const collectionsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/collections",
  component: ProtectedCollections,
});

const ProtectedCollectionDetail = protectedPage(CollectionDetailPage);
const collectionRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/collections/$id",
  component: function CollectionRoute() {
    const { id } = collectionRoute.useParams();
    return <ProtectedCollectionDetail id={id} />;
  },
});

const ProtectedTags = protectedPage(TagsPage);
const tagsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tags",
  component: ProtectedTags,
});

const ProtectedImport = protectedPage(ImportPage);
const importRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/import",
  component: ProtectedImport,
});

const ProtectedSettings = protectedPage(SettingsPage);
const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/settings",
  component: ProtectedSettings,
});

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  component: Login,
});

const enrollRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/enroll",
  validateSearch: (search: Record<string, unknown>): { token: string } => ({
    token: typeof search.token === "string" ? search.token : "",
  }),
  component: function EnrollRoute() {
    const { token } = enrollRoute.useSearch();
    return <Enroll token={token} />;
  },
});

const routeTree = rootRoute.addChildren([
  indexRoute,
  comicRoute,
  collectionsRoute,
  collectionRoute,
  tagsRoute,
  importRoute,
  settingsRoute,
  loginRoute,
  enrollRoute,
]);

export const router = createRouter({ routeTree });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
