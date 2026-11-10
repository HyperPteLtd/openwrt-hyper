import { useEffect, useRef } from "preact/hooks";

/**
 * Call `fn` now and every `ms` milliseconds while the page is visible. In a
 * background tab (the LuCI iframe follows its tab) polling stops, so an
 * open-but-unwatched page costs the router nothing; it resumes with an
 * immediate call when the tab is shown again. `resumed` is true on that first
 * call after a pause (and on the very first call), so rate calculations can
 * drop the stale previous sample.
 */
export function usePoll(fn: (resumed: boolean) => void, ms: number, active = true): void {
  const latest = useRef(fn);
  latest.current = fn;

  useEffect(() => {
    if (!active) return;
    let id: ReturnType<typeof setInterval> | undefined;
    const start = () => {
      if (id !== undefined) return;
      latest.current(true);
      id = setInterval(() => latest.current(false), ms);
    };
    const stop = () => {
      if (id === undefined) return;
      clearInterval(id);
      id = undefined;
    };
    const onVisibility = () => (document.hidden ? stop() : start());

    if (!document.hidden) start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [ms, active]);
}
