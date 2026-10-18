import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query";
import { createRootRoute, createRoute, createRouter, Outlet, RouterProvider, useNavigate, useRouterState } from "@tanstack/react-router";
import "./styles.css";
import { api, ApiError } from "./lib/api";
import { useLiveEvents } from "./lib/live";
import type { Me } from "./lib/types";
import { InvitePage, LoginPage, SetupPage } from "./pages/Auth";
import { Shell } from "./pages/Shell";
import { HostsPage } from "./pages/Hosts";
import { HostDetailPage } from "./pages/HostDetail";
import { AuditPage } from "./pages/Audit";
import { ContainersPage } from "./pages/Containers";
import { ContainerDetailPage } from "./pages/ContainerDetail";
import { StacksPage } from "./pages/Stacks";
import { StackDetailPage } from "./pages/StackDetail";
import { ImagesPage, NetworksPage, VolumesPage } from "./pages/Resources";
import { AlertsPage } from "./pages/Alerts";
import { HostSystemPage } from "./pages/HostSystem";
import { UsersPage } from "./pages/Users";
import { AccountPage } from "./pages/Account";
import { UpdatesPage } from "./pages/Updates";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      retry: (n, err) => !(err instanceof ApiError && [401, 403, 404].includes(err.status)) && n < 2,
    },
  },
});

// Any 401 means the session is gone: drop to the login screen.
queryClient.getQueryCache().subscribe((ev) => {
  const err = ev.query.state.error;
  if (err instanceof ApiError && err.status === 401 && ev.query.queryKey[0] !== "me") {
    queryClient.setQueryData(["me"], null);
  }
});

function useMe() {
  return useQuery<Me | null>({
    queryKey: ["me"],
    queryFn: () => api.me().catch((e) => (e instanceof ApiError && e.status === 401 ? null : Promise.reject(e))),
    staleTime: Infinity,
  });
}

function Gate() {
  const qc = useQueryClient();
  const setup = useQuery({ queryKey: ["setup"], queryFn: api.setupStatus, staleTime: Infinity });
  const me = useMe();
  useLiveEvents(qc, !!me.data);
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();

  // Invitation links work without a session.
  if (pathname.startsWith("/invite/")) {
    return <InvitePage token={decodeURIComponent(pathname.slice("/invite/".length))} onDone={() => navigate({ to: "/" })} />;
  }

  if (setup.isPending || me.isPending) {
    return <div className="label grid min-h-screen place-items-center">inicjalizacja…</div>;
  }
  if (setup.data?.needs_setup) return <SetupPage />;
  if (!me.data) return <LoginPage />;
  return <Shell me={me.data} />;
}

// Pages read the session from the cache; Gate guarantees it is present.
function withMe(Page: (p: { me: Me }) => React.ReactNode) {
  return function Wrapped() {
    const me = queryClient.getQueryData<Me>(["me"])!;
    return <Page me={me} />;
  };
}

const rootRoute = createRootRoute({ component: () => <Gate /> });
const hostsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/", component: withMe(HostsPage) });
const hostRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id", component: withMe(HostDetailPage) });
const containersRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id/containers", component: withMe(ContainersPage) });
const containerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/hosts/$id/containers/$cid",
  component: withMe(ContainerDetailPage),
});
const stacksRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id/stacks", component: withMe(StacksPage) });
const stackRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id/stacks/$name", component: withMe(StackDetailPage) });
const imagesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id/images", component: withMe(ImagesPage) });
const networksRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id/networks", component: withMe(NetworksPage) });
const volumesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id/volumes", component: withMe(VolumesPage) });
const alertsRoute = createRoute({ getParentRoute: () => rootRoute, path: "/alerts", component: withMe(AlertsPage) });
const systemRoute = createRoute({ getParentRoute: () => rootRoute, path: "/hosts/$id/system", component: withMe(HostSystemPage) });
const usersRoute = createRoute({ getParentRoute: () => rootRoute, path: "/users", component: withMe(UsersPage) });
const accountRoute = createRoute({ getParentRoute: () => rootRoute, path: "/account", component: withMe(AccountPage) });
const updatesRoute = createRoute({ getParentRoute: () => rootRoute, path: "/updates", component: withMe(UpdatesPage) });
const auditRoute = createRoute({ getParentRoute: () => rootRoute, path: "/audit", component: AuditPage });

const router = createRouter({
  routeTree: rootRoute.addChildren([hostsRoute,
    hostRoute,
    containersRoute,
    containerRoute,
    stacksRoute,
    stackRoute,
    imagesRoute,
    networksRoute,
    volumesRoute,
    alertsRoute,
    systemRoute,
    usersRoute,
    accountRoute,
    updatesRoute,
    auditRoute,
  ]),
  defaultNotFoundComponent: () => <Outlet />,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
