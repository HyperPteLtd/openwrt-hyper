/** Small standard-library helpers shared by disposable-router integration tests. */
import { spawn, spawnSync } from 'node:child_process';
import { accessSync, constants } from 'node:fs';
import { join } from 'node:path';
import { setTimeout as sleep } from 'node:timers/promises';
export { sleep };
export function command(file, args = [], options = {}) {
  const result = spawnSync(file, args, { encoding: 'utf8', timeout: 10000, ...options });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${file} exited ${result.status}: ${result.stderr || result.stdout}`);
  return result.stdout;
}
export function available(file) {
  return (process.env.PATH || '').split(':').some(dir => {
    try { accessSync(join(dir, file), constants.X_OK); return true; } catch { return false; }
  });
}
export async function until(predicate, description, timeout = 8000, interval = 30) {
  const deadline = performance.now() + timeout;
  do {
    if (await predicate()) return;
    await sleep(interval);
  } while (performance.now() < deadline);
  throw new Error(description);
}
export function launch(file, args = [], options = {}) {
  const child = spawn(file, args, { stdio: 'inherit', ...options });
  // Register immediately so very short-lived commands cannot race a later wait.
  child.finished = new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('exit', (code, signal) => resolve({ code, signal }));
  });
  child.finished.catch(() => {});
  return child;
}
export async function wait(child, timeout = 10000) {
  let timer;
  try {
    return await Promise.race([child.finished, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(`process ${child.pid} timed out`)), timeout);
    })]);
  } finally { clearTimeout(timer); }
}
export async function stop(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  child.kill('SIGTERM');
  try { await wait(child, 5000); } catch { child.kill('SIGKILL'); await child.finished; }
}
