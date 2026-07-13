import { useEffect, useState } from "react";
import {
  ChartNoAxesCombined,
	Cpu,
  FlaskConical,
  LayoutDashboard,
  ShieldCheck,
  Workflow,
} from "lucide-react";

import type { HealthStatus } from "../../shared/api/types";
import type { StreamStatus } from "../../shared/hooks/useEventStream";

const NAV_ITEMS = [
  { id: "overview", label: "总览", icon: LayoutDashboard },
	{ id: "token-usage", label: "模型", icon: Cpu },
  { id: "market", label: "市场", icon: ChartNoAxesCombined },
  { id: "operations", label: "决策", icon: Workflow },
  { id: "portfolio", label: "风控", icon: ShieldCheck },
  { id: "backtest", label: "实验室", icon: FlaskConical },
] as const;

interface AppSidebarProps {
  health: HealthStatus | null;
  streamStatus: StreamStatus;
  pendingCount: number;
}

export function AppSidebar({ health, streamStatus, pendingCount }: AppSidebarProps) {
  const [active, setActive] = useState("overview");

  useEffect(() => {
    const sections = NAV_ITEMS
      .map((item) => document.getElementById(item.id))
      .filter((item): item is HTMLElement => Boolean(item));
    if (!sections.length || !("IntersectionObserver" in window)) return undefined;
    const observer = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((entry) => entry.isIntersecting)
          .sort((left, right) => right.intersectionRatio - left.intersectionRatio)[0];
        if (visible?.target.id) setActive(visible.target.id);
      },
      { rootMargin: "-18% 0px -68% 0px", threshold: [0, 0.15, 0.35] },
    );
    sections.forEach((section) => observer.observe(section));
    return () => observer.disconnect();
  }, []);

  const online = streamStatus === "open" && !health?.kill;

  return (
    <aside className="app-sidebar" aria-label="主导航">
      <a className="sidebar-brand" href="#overview" aria-label="cyp-agent 控制台首页">
        <span className="sidebar-brand__mark" aria-hidden="true">C</span>
        <span>
          <strong>cyp-agent</strong>
          <small>Decision OS</small>
        </span>
      </a>

      <nav className="sidebar-nav">
        <span className="sidebar-nav__label">工作区</span>
        {NAV_ITEMS.map((item) => {
          const Icon = item.icon;
          return (
            <a
              key={item.id}
              href={`#${item.id}`}
              className={active === item.id ? "is-active" : ""}
              onClick={() => setActive(item.id)}
            >
              <Icon size={18} />
              <span>{item.label}</span>
              {item.id === "operations" && pendingCount > 0 ? <b>{pendingCount}</b> : null}
            </a>
          );
        })}
      </nav>

      <div className="sidebar-system">
        <div className={`sidebar-system__pulse ${online ? "is-online" : ""}`}><span /></div>
        <div>
          <strong>{health?.kill ? "安全停机中" : online ? "系统在线" : "正在连接"}</strong>
          <span>{health?.display_mode ?? health?.mode ?? "等待运行时"}</span>
        </div>
      </div>
    </aside>
  );
}
