import { type Signal, signal } from "@preact/signals";
import { useMemo } from "preact/hooks";

/**
 * A shallow reactive object: every top-level property is a signal, so a
 * component that reads `store.x` while rendering re-renders when
 * `store.x = v` is assigned. Replace nested objects and arrays instead of
 * mutating them in place.
 */
export function createStore<T extends object>(init: T): T {
  const sigs = new Map<PropertyKey, Signal<unknown>>();
  for (const [k, v] of Object.entries(init)) sigs.set(k, signal(v));
  return new Proxy({} as T, {
    get: (_, k) => sigs.get(k)?.value,
    set: (_, k, v) => {
      const s = sigs.get(k);
      if (s) s.value = v;
      else sigs.set(k, signal(v));
      return true;
    },
    has: (_, k) => sigs.has(k),
    ownKeys: () => [...sigs.keys()] as (string | symbol)[],
    getOwnPropertyDescriptor: (_, k) =>
      sigs.has(k) ? { value: sigs.get(k)!.value, writable: true, enumerable: true, configurable: true } : undefined,
  });
}

/** createStore() kept for the lifetime of the component */
export function useStore<T extends object>(init: T): T {
  return useMemo(() => createStore(init), []);
}
