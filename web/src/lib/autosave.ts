/** Serialized writes that keep the latest edit while a request is in flight.
 * Queues live outside pages so navigation never abandons pending writes.
 */
export class AutosaveQueue<T> {
  private pending: T | null = null;
  private active: T | null = null;
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
    this.pending = this.pending === null ? value : this.merge(this.pending, value);
    this.error = null;
    this.revision++;
    clearTimeout(this.timer);
    this.timer = undefined;
    if (delay > 0) this.timer = setTimeout(() => { this.timer = undefined; void this.drain(); }, delay);
    this.emit();
    if (!this.timer) void this.drain();
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
    if (this.active !== null || this.timer || this.error) return;
    while (this.pending !== null && !this.timer && !this.error) {
      const value = this.pending;
      this.pending = null;
      this.active = value;
      this.emit();
      try {
        await this.write(value);
        this.savedRevision++;
      } catch (error) {
        // Preserve unsaved edits, including newer input, for an explicit retry.
        this.pending = this.pending === null ? value : this.merge(value, this.pending);
        this.error = error instanceof Error ? error : new Error(String(error));
      }
      this.active = null;
      this.emit();
    }
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
