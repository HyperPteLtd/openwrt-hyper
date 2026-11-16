import { AutosaveQueue } from "./autosave";
import { app } from "./app";
import { t as tr } from "./i18n";
import { call } from "./rpc";
import type { Result } from "./types";

let writing = Promise.resolve();
function write(method: string, args: Record<string, unknown>): Promise<void> {
  const task = writing.then(async () => {
    const result = await call<Result>(method, args);
    if (!result.result) throw new Error(result.error || tr("保存失败"));
    app.tick++;
  });
  // All preferences share UCI files; serialize across pages as well as within a field.
  writing = task.catch(() => {});
  return task;
}

export const configWrites = new AutosaveQueue<Record<string, string>>(
  (values) => write("set_config", { values }),
  (older, newer) => ({ ...older, ...newer }),
);

export const listWrites = {
  direct_list: new AutosaveQueue<string>((content) => write("acllist_write", { type: "direct_list", content })),
  proxy_list: new AutosaveQueue<string>((content) => write("acllist_write", { type: "proxy_list", content })),
};

export const ruleWrites = new AutosaveQueue<Record<string, boolean>>(async (values) => {
  for (const [key, enabled] of Object.entries(values)) await write("set_rule_enabled", { key, enabled });
}, (older, newer) => ({ ...older, ...newer }));

export interface DevicePreferences { mode: "all" | "pick"; direct_macs: string[] }
export const deviceWrites = new AutosaveQueue<DevicePreferences>((value) => write("set_access_control", {
  mode: value.mode,
  direct_macs: value.mode === "pick" ? value.direct_macs : [],
}));

export async function flushPreferences(): Promise<void> {
  const results = await Promise.allSettled([configWrites, ruleWrites, deviceWrites, ...Object.values(listWrites)].map((queue) => queue.flush()));
  const failed = results.find((result) => result.status === "rejected");
  if (failed?.status === "rejected") throw failed.reason;
}
