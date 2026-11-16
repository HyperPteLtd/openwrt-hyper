export interface RpcTaskState<T> {
  state: string;
  data?: T;
  error?: string;
}

/** Only the two server-defined deadlines are accepted from the acknowledgment. */
export async function waitForRpcTask<T>(
  timeout: unknown,
  read: () => Promise<RpcTaskState<T>>,
  checkIdentity: () => void,
  clock: { now: () => number; sleep: (ms: number) => Promise<unknown> } = {
    now: Date.now,
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  },
): Promise<T> {
  const deadline = clock.now() + ((timeout === 480 ? 480 : 180) + 5) * 1000;
  while (clock.now() < deadline) {
    checkIdentity();
    await clock.sleep(250);
    checkIdentity();
    if (clock.now() >= deadline) break;
    const result = await read();
    checkIdentity();
    if (result.state === "done") return result.data as T;
    if (result.state === "failed") throw new Error(result.error || "task_failed");
  }
  throw new Error("timeout");
}
