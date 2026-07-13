import { useCallback, useEffect, useRef, useState } from "react";

export interface ResourceState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  refresh: () => Promise<void>;
}

export function usePollingResource<T>(
  loader: () => Promise<T>,
  intervalMs = 0,
): ResourceState<T> {
  const loaderRef = useRef(loader);
  const mountedRef = useRef(true);
  const inFlightRef = useRef<Promise<void> | null>(null);
  const serializedRef = useRef<string | null>(null);
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    loaderRef.current = loader;
  }, [loader]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const refresh = useCallback(() => {
    if (inFlightRef.current) return inFlightRef.current;
    const task = (async () => {
      try {
        const next = await loaderRef.current();
        if (!mountedRef.current) return;
        const serialized = JSON.stringify(next);
        if (serializedRef.current !== serialized) {
          serializedRef.current = serialized;
          setData(next);
        }
        setError(null);
      } catch (err) {
        if (!mountedRef.current) return;
        setError(err instanceof Error ? err.message : "请求失败");
      } finally {
        if (mountedRef.current) setLoading(false);
      }
    })();
    inFlightRef.current = task;
    void task.finally(() => {
      if (inFlightRef.current === task) inFlightRef.current = null;
    });
    return task;
  }, []);

  useEffect(() => {
    void refresh();
    if (!intervalMs) return undefined;
    let stopped = false;
    let timer: number | null = null;
    const schedule = () => {
      if (stopped || timer !== null) return;
      timer = window.setTimeout(() => {
        timer = null;
        if (document.visibilityState === "hidden" || !navigator.onLine) return;
        void refresh().finally(schedule);
      }, intervalMs);
    };
    const resume = () => {
      if (document.visibilityState === "hidden" || !navigator.onLine || timer !== null) return;
      void refresh().finally(schedule);
    };
    schedule();
    document.addEventListener("visibilitychange", resume);
    window.addEventListener("online", resume);
    return () => {
      stopped = true;
      if (timer !== null) window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", resume);
      window.removeEventListener("online", resume);
    };
  }, [intervalMs, refresh]);

  return { data, error, loading, refresh };
}
