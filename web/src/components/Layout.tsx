import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation } from "react-router";
import { api } from "../api/client";
import { Icon, type IconName } from "./Icon";

type Theme = "light" | "dark" | null;

function readTheme(): Theme {
  try {
    const t = localStorage.getItem("ossp-theme");
    return t === "light" || t === "dark" ? t : null;
  } catch {
    return null;
  }
}

/** Follows the OS until the viewer picks; the pick is remembered per browser. */
function useTheme() {
  const [theme, setTheme] = useState<Theme>(readTheme);
  useEffect(() => {
    const root = document.documentElement;
    if (theme) root.dataset.theme = theme;
    else delete root.dataset.theme;
    try {
      if (theme) localStorage.setItem("ossp-theme", theme);
    } catch {
      // Storage blocked: the choice lasts for this tab only, which is fine.
    }
  }, [theme]);
  const dark = theme ? theme === "dark" : window.matchMedia?.("(prefers-color-scheme: dark)").matches;
  return { dark, toggle: () => setTheme(dark ? "light" : "dark") };
}

function Item({ to, icon, label, count, end }: { to: string; icon: IconName; label: string; count?: number; end?: boolean }) {
  return (
    <NavLink to={to} end={end} className="nav-item">
      <Icon name={icon} />
      <span>{label}</span>
      {count ? <span className="nav-count" aria-label={`${count} waiting`}>{count}</span> : null}
    </NavLink>
  );
}

export function Layout() {
  const { dark, toggle } = useTheme();
  const overview = useQuery({ queryKey: ["overview"], queryFn: api.overview });
  const { pathname } = useLocation();

  // Move focus to the page heading on navigation, so a screen reader announces
  // the new page instead of staying on the link that was activated.
  useEffect(() => {
    document.querySelector<HTMLElement>("main h1")?.focus({ preventScroll: true });
    window.scrollTo(0, 0);
  }, [pathname]);

  return (
    <div className="shell">
      <a className="skip" href="#main">Skip to content</a>
      <header className="sidebar">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true">
            <Icon name="pr" size={18} />
          </span>
          <div>
            <p className="brand-name">OSS Pipeline</p>
            <p className="brand-sub">contributions as vimalyad</p>
          </div>
        </div>
        <nav aria-label="Main">
          <Item to="/" end icon="home" label="Overview" />
          <Item to="/proposals" icon="inbox" label="Proposals" count={overview.data?.awaitingApproval} />
          <Item to="/prs" icon="pr" label="Pull requests" />
          <Item to="/candidates" icon="list" label="Candidates" />
          <Item to="/audit" icon="log" label="Audit log" />
        </nav>
        <button type="button" className="btn btn-ghost theme-toggle" onClick={toggle}>
          <Icon name={dark ? "sun" : "moon"} />
          <span className="theme-label">{dark ? "Light theme" : "Dark theme"}</span>
        </button>
      </header>
      <main id="main" className="main">
        <Outlet />
      </main>
    </div>
  );
}

export function PageHead({ title, sub, children }: { title: string; sub?: string; children?: React.ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1 tabIndex={-1}>{title}</h1>
        {sub && <p className="page-sub">{sub}</p>}
      </div>
      {children && <div className="page-tools">{children}</div>}
    </div>
  );
}
