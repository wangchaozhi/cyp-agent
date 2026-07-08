import { useEffect, useRef, useState } from "react";

import type { DashboardEvent } from "../api/types";

export type StreamStatus = "connecting" | "open" | "reconnecting" | "closed";

export function useEventStream(onEvent: (event: DashboardEvent) => void): StreamStatus {
  const onEventRef = useRef(onEvent);
  const [status, setStatus] = useState<StreamStatus>("connecting");

  useEffect(() => {
    onEventRef.current = onEvent;
  }, [onEvent]);

  useEffect(() => {
    const source = new EventSource("/api/events");
    setStatus("connecting");

    source.onopen = () => setStatus("open");
    source.onerror = () => {
      setStatus(source.readyState === EventSource.CLOSED ? "closed" : "reconnecting");
    };
    source.onmessage = (message) => {
      if (!message.data.trim()) return;
      try {
        onEventRef.current(JSON.parse(message.data) as DashboardEvent);
      } catch {
        // Ignore malformed SSE frames; the next event can still be valid.
      }
    };

    return () => {
      source.close();
      setStatus("closed");
    };
  }, []);

  return status;
}
