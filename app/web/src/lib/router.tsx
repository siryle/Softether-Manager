"use client";

/**
 * A hash router.
 *
 * The export is one HTML file served under a prefix chosen at install time,
 * so path routing would 404 on every deep link the server has never heard of.
 * The hash never reaches the server: `/#/server/3/hub/VPN/users` works from a
 * bookmark, a refresh, and any mount point, with zero server cooperation.
 */
import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type MouseEvent,
  type ReactNode,
} from "react";

export interface Route {
  /** The hash path, normalised: always starts with "/", no trailing slash. */
  path: string;
  parts: string[];
}

function parse(): Route {
  if (typeof window === "undefined") return { path: "/", parts: [] };
  let raw = window.location.hash.replace(/^#/, "");
  if (!raw.startsWith("/")) raw = "/" + raw;
  raw = raw !== "/" ? raw.replace(/\/+$/, "") : raw;
  const parts = raw.split("/").filter(Boolean).map(decodeURIComponent);
  return { path: raw, parts };
}

const RouteContext = createContext<Route>({ path: "/", parts: [] });

export function RouterProvider({ children }: { children: ReactNode }) {
  const [route, setRoute] = useState<Route>(parse);
  const prevPathRef = useRef(route.path);

  useEffect(() => {
    const onChange = () => setRoute(parse());
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);

  useLayoutEffect(() => {
    const prevPath = prevPathRef.current;
    if (prevPath === route.path) return;
    prevPathRef.current = route.path;

    const prevRoot = prevPath.split("/")[1] ?? "";
    const nextRoot = route.parts[0] ?? "";
    const hasDocSection = (nextRoot === "settings" || nextRoot === "server-settings") && Boolean(route.parts[1]);
    if (hasDocSection && prevRoot === nextRoot) return;

    const resetTop = () => {
      window.scrollTo({ top: 0, left: 0, behavior: "auto" });
      document.documentElement.scrollTop = 0;
      document.body.scrollTop = 0;
    };
    resetTop();

    // Safari/WebKit can apply native hash-anchor scroll adjustments one frame after hashchange.
    if (!hasDocSection) {
      const frame = requestAnimationFrame(() => {
        if (window.scrollY > 0 || document.documentElement.scrollTop > 0) resetTop();
      });
      return () => cancelAnimationFrame(frame);
    }
  }, [route]);

  return <RouteContext.Provider value={route}>{children}</RouteContext.Provider>;
}

export const useRoute = () => useContext(RouteContext);

export function navigate(path: string) {
  window.location.hash = path.startsWith("/") ? path : "/" + path;
}

export function back() {
  window.history.back();
}

/** An anchor that stays a real link (open-in-new-tab works) but routes by hash. */
export function Link({
  to,
  className,
  children,
  onClick,
  ...rest
}: {
  to: string;
  className?: string;
  children: ReactNode;
  onClick?: (e: MouseEvent<HTMLAnchorElement>) => void;
} & Record<string, unknown>) {
  return (
    <a href={`#${to}`} className={className} onClick={onClick} {...rest}>
      {children}
    </a>
  );
}

/** Encode one path segment so hub and user names survive slashes and spaces. */
export const seg = encodeURIComponent;
