"use client";

import { AuthProvider, useAuth } from "./lib/auth";
import { ServerProvider, useServer } from "./lib/server";
import { RouterProvider, useRoute } from "./lib/router";
import { ToastProvider } from "./lib/toast";
import { RatesProvider } from "./lib/rates";
import { ThemeProvider } from "./ui/theme";
import { I18nProvider } from "./lib/i18n";
import { AppShell } from "./components/AppShell";
import { Login } from "./screens/Login";
import { Dashboard } from "./screens/Dashboard";
import { AllUsers } from "./screens/AllUsers";
import { Connect } from "./screens/Connect";
import { ServerSettings } from "./screens/ServerSettings";
import { AllClients } from "./screens/AllClients";
import { Logs } from "./screens/Logs";
import { HubDetail } from "./screens/HubDetail";
import { Hubs } from "./screens/Hubs";
import { UserDetail } from "./screens/UserDetail";
import { Settings } from "./screens/Settings";
import { Servers } from "./screens/Servers";
import { ErrorAlert } from "./components/bits";
import { useI18n } from "./lib/i18n";

export default function App() {
  return (
    <RouterProvider>
      <I18nProvider>
        <ThemeProvider>
          <ToastProvider>
            <AuthProvider>
              <RatesProvider>
                <ServerProvider>
                  <Routed />
                </ServerProvider>
              </RatesProvider>
            </AuthProvider>
          </ToastProvider>
        </ThemeProvider>
      </I18nProvider>
    </RouterProvider>
  );
}

/**
 * The route table. Hash paths:
 *
 *   /                         the dashboard: this machine + its SoftEther
 *   /connect                  connect the panel to the local SoftEther
 *   /users                    every user, across all hubs
 *   /hubs                     all Virtual Hubs
 *   /hub/:hub[/:tab]          one Virtual Hub
 *   /hub/:hub/user/:name      one user
 *   /server-settings[/:sec]   server-wide SoftEther settings
 *   /connections              TCP connections into the server
 *   /logs                     log browser
 *   /settings[/:section]      the panel itself
 */
function Routed() {
  const { user, loading } = useAuth();
  const route = useRoute();
  const { servers, currentServerId, error, refreshServers, refresh } = useServer();
  const { t } = useI18n();

  if (loading) return <div className="loading" />;
  if (!user) return <Login />;

  if (!servers) return error ? <div className="page"><ErrorAlert>{error}</ErrorAlert><button className="btn" onClick={() => void refreshServers().then(() => refresh())}>{t("Retry")}</button></div> : <div className="loading" />;

  const p = route.parts;
  let screen: React.ReactNode = <Dashboard />;

  if (p[0] === "connect") {
    screen = <Connect />;
  } else if (p[0] === "servers") {
    screen = <Servers />;
  } else if (p[0] === "users") {
    screen = <AllUsers />;
  } else if (p[0] === "hubs") {
    screen = <Hubs />;
  } else if (p[0] === "settings") {
    screen = <Settings section={p[1]} />;
  } else if (p[0] === "server-settings") {
    screen = <ServerSettings section={p[1]} />;
  } else if (p[0] === "clients" || p[0] === "connections") {
    screen = <AllClients />;
  } else if (p[0] === "logs") {
    screen = <Logs />;

  } else if (p[0] === "hub" && p[1]) {
    const hub = p[1];
    if (p[2] === "user" && p[3]) {
      screen = <UserDetail key={JSON.stringify([hub, p[3]])} hub={hub} name={p[3]} />;
    } else {
      screen = <HubDetail key={hub} hub={hub} tab={p[2] ?? "overview"} />;
    }
  }

  return <AppShell key={currentServerId}>{screen}</AppShell>;
}
