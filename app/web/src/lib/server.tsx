"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  api,
  getStoredServerId,
  setStoredServerId,
  type ManagedServer,
  type Probe,
  type Wire,
} from "./api";
import { useAuth } from "./auth";

/**
 * The managed server cluster and active instance, held once for the whole app.
 *
 * Supports switching the active server in one click, dynamically routing
 * probes, hub lists and all SoftEther operations to the selected instance.
 */

export type ServerHealth = "probing" | "online" | "offline" | "unconfigured";

export interface ServerApi {
  probe: Probe | null;
  hubs: Wire[] | null;
  health: ServerHealth;
  refresh: () => Promise<void>;
  servers: ManagedServer[] | null;
  currentServer: ManagedServer | null;
  currentServerId: number | null;
  switchServer: (id: number) => Promise<void>;
  refreshServers: () => Promise<ManagedServer[] | null>;
}

const ServerContext = createContext<ServerApi>(null as unknown as ServerApi);

const BACKGROUND_REFRESH_MS = 30_000;

export function ServerProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const [servers, setServers] = useState<ManagedServer[] | null>(null);
  const [currentServerId, setCurrentServerId] = useState<number | null>(null);
  const [probe, setProbe] = useState<Probe | null>(null);
  const [hubs, setHubs] = useState<Wire[] | null>(null);
  const aliveRef = useRef(true);

  const refreshServers = useCallback(async () => {
    try {
      const list = await api.servers();
      if (!aliveRef.current) return null;
      setServers(list);

      const storedId = getStoredServerId();
      let matched = list.find((s) => s.id === storedId);
      if (!matched && list.length > 0) {
        matched = list.find((s) => s.is_default) || list[0];
      }
      if (matched) {
        setStoredServerId(matched.id);
        setCurrentServerId(matched.id);
      }
      return list;
    } catch {
      return null;
    }
  }, []);

  const refresh = useCallback(async () => {
    let next: Probe;
    try {
      next = await api.probe();
    } catch {
      return; // signed out, or the panel itself is unreachable; keep what we have
    }
    if (!aliveRef.current) return;
    setProbe(next);
    if (next.online) {
      const list = await api.hubs().catch(() => null);
      if (aliveRef.current && list) setHubs((list.HubList as Wire[]) ?? []);
    }
  }, []);

  const switchServer = useCallback(
    async (id: number) => {
      setStoredServerId(id);
      setCurrentServerId(id);
      setProbe(null);
      setHubs(null);
      await refresh();
      void refreshServers();
    },
    [refresh, refreshServers],
  );

  useEffect(() => {
    aliveRef.current = true;
    if (!user) {
      setServers(null);
      setCurrentServerId(null);
      setProbe(null);
      setHubs(null);
      return;
    }

    void refreshServers().then(() => {
      void refresh();
    });

    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") {
        void refresh();
        void refreshServers();
      }
    }, BACKGROUND_REFRESH_MS);

    return () => {
      aliveRef.current = false;
      window.clearInterval(timer);
    };
  }, [user, refresh, refreshServers]);

  const currentServer =
    servers?.find((s) => s.id === currentServerId) ?? servers?.[0] ?? null;

  const health: ServerHealth =
    probe === null
      ? "probing"
      : !probe.configured
        ? "unconfigured"
        : probe.online
          ? "online"
          : "offline";

  return (
    <ServerContext.Provider
      value={{
        probe,
        hubs,
        health,
        refresh,
        servers,
        currentServer,
        currentServerId,
        switchServer,
        refreshServers,
      }}
    >
      {children}
    </ServerContext.Provider>
  );
}

export const useServer = () => useContext(ServerContext);
