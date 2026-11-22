import { AutosaveQueue } from "./autosave";
import { app } from "./app";
import { saveLanguage, t as tr, type LanguagePreference } from "./i18n";
import { call } from "./rpc";
import type { Result } from "./types";
import { accountEpoch, onAccountChange } from "./account-epoch";
import { CacheInvalidatedError } from "./cache";
import { errorText } from "./errors";

let writing = Promise.resolve();
export function writePreference<T extends Result = Result>(method: string, args: Record<string, unknown>, epoch = accountEpoch()): Promise<T> {
  const task = writing.then(async () => {
    if (epoch !== accountEpoch()) throw new CacheInvalidatedError();
    const result = await call<T>(method, args);
    if (epoch !== accountEpoch()) throw new CacheInvalidatedError();
    if (!result.result) throw new Error(errorText(result.error) || result.msg || tr("保存失败"));
    app.tick++;
    return result;
  });
  // All preferences share UCI files; serialize across pages as well as within a field.
  writing = task.then(() => {}, () => {});
  return task;
}

async function write(method: string, args: Record<string, unknown>, epoch = accountEpoch()): Promise<void> {
  await writePreference(method, args, epoch);
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
  const epoch = accountEpoch();
  for (const [key, enabled] of Object.entries(values)) await write("set_rule_enabled", { key, enabled }, epoch);
}, (older, newer) => ({ ...older, ...newer }));

export interface DevicePreferences { mode: "all" | "pick"; direct_macs: string[] }
export const deviceWrites = new AutosaveQueue<DevicePreferences>((value) => write("set_access_control", {
  mode: value.mode,
  direct_macs: value.mode === "pick" ? value.direct_macs : [],
}));

for (const queue of [configWrites, ruleWrites, deviceWrites, ...Object.values(listWrites)]) {
  let revision = queue.revision;
  queue.subscribe(() => {
    if (revision !== queue.revision) {
      revision = queue.revision;
      app.intentRevision++;
    }
  });
}

export async function flushPreferences(): Promise<void> {
  const queues = [configWrites, ruleWrites, deviceWrites, ...Object.values(listWrites)];
  do {
    const results = await Promise.allSettled(queues.map((queue) => queue.flush()));
    const failed = results.find((result) => result.status === "rejected");
    if (failed?.status === "rejected") throw failed.reason;
  } while (queues.some(queue => queue.busy));
}

let languageRevision = 0;
export let pendingLanguagePreference: LanguagePreference | null = null;
onAccountChange(() => {
  languageRevision++;
  pendingLanguagePreference = null;
});
export async function requestLanguage(preference: LanguagePreference): Promise<void> {
  const revision = ++languageRevision;
  pendingLanguagePreference = preference;
  app.intentRevision++;
  try {
    await flushPreferences();
    if (revision !== languageRevision) return;
    saveLanguage(preference);
    pendingLanguagePreference = null;
    location.reload();
  } catch (error) {
    if (revision !== languageRevision) return;
    pendingLanguagePreference = null;
    throw error;
  }
}
