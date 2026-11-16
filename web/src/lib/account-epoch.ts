/** Queued UI work belongs to the account that existed when it was requested. */
let epoch = 0;
const listeners = new Set<() => void>();
export function accountEpoch(): number { return epoch; }
export function onAccountChange(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}
export function invalidateAccountWork(): void {
  epoch++;
  for (const listener of listeners) listener();
}
