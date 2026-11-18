"use client";

import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { api, getStoredServerId, setStoredServerId, type ManagedServer, type Probe, type Wire } from "./api";
import { useAuth } from "./auth";
import { useRate } from "./rates";
import { navigate } from "./router";

export type ServerHealth = "probing" | "online" | "offline" | "unconfigured";
export interface ServerApi {
  probe: Probe | null;
  hubs: Wire[] | null;
  clientCount: number | null;
  resources: Wire | null;
  health: ServerHealth;
  servers: ManagedServer[] | null;
  currentServer: ManagedServer | null;
  currentServerId: number | null;
  error: string | null;
  refresh: () => Promise<void>;
  switchServer: (id: number) => Promise<void>;
  refreshServers: () => Promise<ManagedServer[] | null>;
  setClientCount: (count: number | null) => void;
}
const ServerContext = createContext<ServerApi>(null as unknown as ServerApi);

export function ServerProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth();
  const [servers, setServers] = useState<ManagedServer[] | null>(null);
  const [currentServerId, setCurrentServerId] = useState<number | null>(null);
  const [probe, setProbe] = useState<Probe | null>(null);
  const [hubs, setHubs] = useState<Wire[] | null>(null);
  const [clientCount, setClientCount] = useState<number | null>(null);
  const [resources, setResources] = useState<Wire | null>(null);
  const [error, setError] = useState<string | null>(null);
  const liveMs = useRate("live");
  const alive = useRef(false);
  const generation = useRef(0);

  const refresh = useCallback(async () => {
    const id = getStoredServerId();
    if (!id) return;
    const requestGeneration = generation.current;
    const current = () => alive.current && generation.current === requestGeneration && getStoredServerId() === id;
    try {
      const next = await api.probe();
      if (!current()) return;
      setProbe(next);
      if (!next.online) { setHubs(null); setClientCount(null); return; }
      const [list, clientsRes] = await Promise.all([
        api.hubs().catch(() => null),
        api.allClients().catch(() => null),
      ]);
      if (current()) {
        setHubs(list ? (list.HubList as Wire[]) ?? [] : null);
        if (clientsRes && Array.isArray(clientsRes.clients)) {
          setClientCount(clientsRes.clients.length);
        }
      }
    } catch (e) {
      if (current()) {
        setProbe({ online: false, configured: true, error: e instanceof Error ? e.message : String(e) });
        setHubs(null);
        setClientCount(null);
      }
    }
  }, []);

  const refreshServers = useCallback(async () => {
    const requestGeneration = generation.current;
    try {
      const list = await api.servers();
      if (!alive.current || generation.current !== requestGeneration) return null;
      setServers(list);
      setError(null);
      const storedId = getStoredServerId();
      const next = list.find(s => s.id === storedId && s.is_enabled)
        ?? list.find(s => s.is_default && s.is_enabled)
        ?? list.find(s => s.is_enabled);
      if (next && next.id !== storedId) {
        generation.current++;
        setProbe(null);
        setHubs(null);
        setClientCount(null);
      }
      if (next) {
        setStoredServerId(next.id);
        setCurrentServerId(next.id);
      }
      return list;
    } catch (e) {
      if (alive.current && generation.current === requestGeneration) setError(e instanceof Error ? e.message : String(e));
      return null;
    }
  }, []);

  const switchServer = useCallback(async (id: number) => {
    if (!servers?.some(s => s.id === id && s.is_enabled)) throw new Error("Server is unavailable.");
    generation.current++;
    setStoredServerId(id);
    setProbe(null);
    setHubs(null);
    setClientCount(null);
    setCurrentServerId(id);
    navigate("/");
    void refresh();
  }, [servers, refresh]);

  useEffect(() => {
    alive.current = true;
    generation.current++;
    if (!user) {
      setServers(null); setCurrentServerId(null); setProbe(null); setHubs(null); setClientCount(null); setResources(null); setError(null);
    } else {
      void refreshServers().then(list => { if (list) void refresh(); });
    }
    const timer = user ? window.setInterval(() => {
      if (document.visibilityState === "visible") void refreshServers().then(list => { if (list) void refresh(); });
    }, 30_000) : undefined;
    return () => { alive.current = false; generation.current++; window.clearInterval(timer); };
  }, [user, refresh, refreshServers]);

  useEffect(() => {
    if (!user) return;
    let active = true;
    let inFlight = false;
    const poll = async () => {
      if (!active || inFlight) return;
      inFlight = true;
      try {
        const res = await api.resources().catch(() => null);
        if (active) setResources(res);
      } finally {
        inFlight = false;
      }
    };
    void poll();
    const timer = window.setInterval(() => {
      if (document.visibilityState === "visible") void poll();
    }, liveMs);
    return () => { active = false; window.clearInterval(timer); };
  }, [user, liveMs]);

  const currentServer = servers?.find(s => s.id === currentServerId) ?? null;
  const health: ServerHealth = probe === null ? "probing" : !probe.configured ? "unconfigured" : probe.online ? "online" : "offline";
  return <ServerContext.Provider value={{ probe, hubs, clientCount, resources, health, servers, currentServer, currentServerId, error, refresh, switchServer, refreshServers, setClientCount }}>{children}</ServerContext.Provider>;
}
export const useServer = () => useContext(ServerContext);
