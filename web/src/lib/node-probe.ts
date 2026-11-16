export interface ProbeReply {
  result: boolean;
  id?: string;
  state?: "queued" | "running" | "done" | "cancelled" | "failed";
  latency?: Record<string, number | null>;
  completed?: number;
  total?: number;
  error?: string;
}
export interface ProbeProgress { running: boolean; completed: number; total: number }
type Rpc = (method: string, args: Record<string, unknown>) => Promise<ProbeReply>;

/** New jobs and user edits invalidate both queued replies and automatic selection. */
export class NodeProbe {
  private revision = 0;
  private job: string | undefined;
  private rpc: Rpc;
  private progress: (value: ProbeProgress) => void;
  private delay: (ms: number) => Promise<void>;
  constructor(rpc: Rpc, progress: (value: ProbeProgress) => void,
    delay = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms))) {
    this.rpc = rpc; this.progress = progress; this.delay = delay;
  }

  cancel(): void {
    this.revision++;
    const id = this.job;
    this.job = undefined;
    this.progress({ running: false, completed: 0, total: 0 });
    if (id) void this.rpc("latency_cancel", { id }).catch(() => {});
  }

  async run(nodes: string[], update: (values: Record<string, number | null>) => void): Promise<Record<string, number | null> | undefined> {
    this.cancel();
    const revision = this.revision;
    const seen = new Set<string>();
    this.progress({ running: true, completed: 0, total: Math.min(nodes.length, 64) });
    try {
      let reply = await this.rpc("latency", { nodes });
      if (revision !== this.revision) {
        if (reply.id) void this.rpc("latency_cancel", { id: reply.id }).catch(() => {});
        return;
      }
      if (!reply.result || !reply.id) throw new Error(reply.error || "probe_start_failed");
      const id = reply.id;
      this.job = id;
      const deadline = Date.now() + 125000;
      while (revision === this.revision) {
        if (!reply.result || reply.state === "failed") throw new Error(reply.error || "probe_failed");
        if (reply.id !== id || reply.state === "cancelled") return;
        const fresh = Object.fromEntries(Object.entries(reply.latency || {}).filter(([name]) => !seen.has(name)));
        for (const name of Object.keys(fresh)) seen.add(name);
        if (Object.keys(fresh).length) update(fresh);
        this.progress({ running: reply.state !== "done", completed: reply.completed || 0, total: reply.total || 0 });
        if (reply.state === "done") return reply.latency || {};
        if (Date.now() >= deadline) throw new Error("probe_timeout");
        await this.delay(250);
        if (revision !== this.revision) return;
        reply = await this.rpc("latency_status", { id });
      }
    } catch (error) {
      if (revision === this.revision) throw error;
    } finally {
      if (revision === this.revision) this.cancel();
    }
  }
}
