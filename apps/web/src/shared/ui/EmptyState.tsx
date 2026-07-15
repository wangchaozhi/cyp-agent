import type { ReactNode } from "react";

interface EmptyStateProps {
  children: ReactNode;
  action?: ReactNode;
  compact?: boolean;
}

export function EmptyState({ children, action, compact = false }: EmptyStateProps) {
  return (
    <div className={`empty-state ${compact ? "empty-state--compact" : ""}`.trim()}>
      <div className="empty-state__content">{children}</div>
      {action ? <div className="empty-state__action">{action}</div> : null}
    </div>
  );
}
