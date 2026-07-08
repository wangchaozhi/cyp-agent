import type { ReactNode } from "react";

interface PanelProps {
  title: string;
  icon?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}

export function Panel({ title, icon, actions, children, className = "" }: PanelProps) {
  return (
    <section className={`panel ${className}`.trim()}>
      <div className="panel__header">
        <div className="panel__title">
          {icon}
          <h2>{title}</h2>
        </div>
        {actions ? <div className="panel__actions">{actions}</div> : null}
      </div>
      <div className="panel__body">{children}</div>
    </section>
  );
}
