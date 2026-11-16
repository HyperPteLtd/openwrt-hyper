import { accountEpoch, onAccountChange } from "./account-epoch.ts";

/** Serialized writes that keep the latest edit while a request is in flight.
 * Queues live outside pages so navigation never abandons pending writes.
 */
export class AutosaveQueue<T> {
  private pending: T | null = null;
  private active: T | null = null;
  private draining = false;
  private generation = 0;
  private pendingEpoch = accountEpoch();
  private timer: ReturnType<typeof setTimeout> | undefined;
  private listeners = new Set<() => void>();
  private write: (value: T) => Promise<void>;
  private merge: (older: T, newer: T) => T;
  error: Error | null = null;
  revision = 0;
  savedRevision = 0;

  constructor(write: (value: T) => Promise<void>, merge: (older: T, newer: T) => T = (_, newer) => newer) {
    this.write = write;
    this.merge = merge;
    onAccountChange(() => this.cancel());
  }

  get busy(): boolean { return this.pending !== null || this.active !== null; }
  get value(): T | null {
    return this.active === null ? this.pending : this.pending === null ? this.active : this.merge(this.active, this.pending);
  }

  subscribe(listener: () => void): () => void {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  }

  private emit(): void { for (const listener of this.listeners) listener(); }

  enqueue(value: T, delay = 0): void {
    this.pendingEpoch = accountEpoch();
    this.pending = this.pending === null ? value : this.merge(this.pending, value);
    this.error = null;
    this.revision++;
    clearTimeout(this.timer);
    this.timer = undefined;
    if (delay > 0) this.timer = setTimeout(() => { this.timer = undefined; void this.drain(); }, delay);
    this.emit();
    if (!this.timer) void this.drain();
  }

  /** Discard account-owned drafts immediately; an old request may only settle. */
  cancel(): void {
    this.generation++;
    this.revision++;
    this.pending = null;
    this.active = null;
    this.error = null;
    this.savedRevision = 0;
    clearTimeout(this.timer);
    this.timer = undefined;
    this.emit();
  }

  /** Stop an earlier, still-debounced valid input from saving after it becomes invalid. */
  discardPending(filter: (value: T) => T | null): void {
    if (this.pending === null) return;
    this.pending = filter(this.pending);
    this.revision++;
    if (this.pending === null) {
      clearTimeout(this.timer);
      this.timer = undefined;
      this.error = null;
    }
    this.emit();
  }

  private async drain(): Promise<void> {
    if (this.draining || this.timer || this.error) return;
    this.draining = true;
    while (this.pending !== null && !this.timer && !this.error) {
      const value = this.pending;
      const epoch = this.pendingEpoch;
      const generation = this.generation;
      this.pending = null;
      if (epoch !== accountEpoch()) continue;
      this.active = value;
      this.emit();
      try {
        await this.write(value);
        if (generation === this.generation && epoch === accountEpoch()) this.savedRevision++;
      } catch (error) {
        if (generation !== this.generation || epoch !== accountEpoch()) continue;
        // A newer edit is already a new attempt: do not let an obsolete failure
        // block it. Preserve failed, unrelated fields in that latest write.
        const hasNewer = this.pending !== null;
        this.pending = this.pending === null ? value : this.merge(value, this.pending);
        if (!hasNewer) this.error = error instanceof Error ? error : new Error(String(error));
      } finally {
        if (generation === this.generation) {
          this.active = null;
          this.emit();
        }
      }
    }
    this.draining = false;
  }

  flush(): Promise<void> {
    clearTimeout(this.timer);
    this.timer = undefined;
    void this.drain();
    return new Promise((resolve, reject) => {
      const settle = () => {
        if (this.error) { unsubscribe(); reject(this.error); }
        else if (!this.busy) { unsubscribe(); resolve(); }
      };
      const unsubscribe = this.subscribe(settle);
      settle();
    });
  }

  retry(): Promise<void> {
    this.error = null;
    return this.flush();
  }
}

export function invalidDomainLine(content: string): string | undefined {
  return content.split(/\r\n?|\n/).map((line) => line.trim()).find((line) => !!line && !/^[A-Za-z0-9._-]{1,253}$/.test(line));
}

/** Validate a complete address before an automatic write, not every typed fragment. */
export function validDnsAddress(value: string): boolean {
  if (value === "wan") return true;
  if (!value || !/^[A-Za-z0-9.:/\[\]_-]{1,255}$/.test(value)) return false;
  try {
    const explicitScheme = value.includes("://");
    const url = new URL(explicitScheme ? value : `udp://${value.includes(":") && !value.startsWith("[") && value.split(":").length > 2 ? `[${value}]` : value}`);
    if (!["https:", "http:", "tls:", "udp:", "tcp:", "quic:", "h3:"].includes(url.protocol)) return false;
    if (!url.hostname || url.username || url.password || url.hash || url.search || (url.port && (+url.port < 1 || +url.port > 65535))) return false;
    if (!explicitScheme && url.pathname && url.pathname !== "/") return false;
    if (url.hostname.startsWith("[")) return true; // URL validates the complete IPv6 literal.
    if (/^[\d.]+$/.test(url.hostname)) {
      // Don't let the URL parser silently expand an incomplete IPv4 address.
      const inputHost = (explicitScheme ? value.slice(value.indexOf("://") + 3) : value).split(/[/:]/)[0];
      return /^(?:\d{1,3}\.){3}\d{1,3}$/.test(inputHost) && inputHost.split(".").every((part) => +part <= 255);
    }
    return url.hostname.includes(".") && url.hostname.split(".").every((part) => /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/.test(part));
  } catch { return false; }
}
