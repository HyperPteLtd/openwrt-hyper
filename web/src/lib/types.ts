/** Shapes returned by luci.hyper (see luci-app-hyper/root/usr/share/rpcd/ucode/luci.hyper) */

export type Mode = "smart" | "global" | "dns" | "custom";

export interface SyncState {
  ok: boolean;
  time: number;
  count?: number;
  added?: number;
  removed?: number;
  error?: string;
  msg?: string;
}

export interface HealthState {
  state: "ok" | "degraded" | "failopen" | "recovering";
  failures: number;
  time: number;
}

export interface Status {
  enabled: boolean;
  running: boolean;
  memory_kb: number | null;
  uptime: number | null;
  main_node: string | null;
  main_node_label: string | null;
  mode: Mode;
  signed_in: boolean;
  email?: string;
  api_base: string;
  sync: SyncState | null;
  health: HealthState | null;
  singbox_version: string | null;
  version: string | null;
}

export interface NodeInfo {
  name: string;
  label: string;
  type: string;
  country?: string;
  city?: string;
  transport?: string;
  reality?: boolean;
}

export interface NodesResult {
  nodes: NodeInfo[];
  main_node: string;
}

export interface Traffic {
  running: boolean;
  upload_total?: number;
  download_total?: number;
  per_source?: Record<string, { upload: number; download: number; connections: number }>;
  error?: string;
}

export interface Result {
  result: boolean;
  error?: string;
  code?: number;
  msg?: string;
}
