import { accountEpoch, onAccountChange } from "./account-epoch.ts";

/** Serialize mutations while keeping only the latest waiting intent.
 * Responses from superseded requests never update the visible value or error.
 */
export class LatestAction<T, R> {
  private pending: { id: number; value: T; epoch: number } | null = null;
  private active = false;
  private revision = 0;
  private write: (value: T) => Promise<R>;
  private accepted: (value: T, result: R) => void;
  private failed: (error: Error) => void;

  constructor(write: (value: T) => Promise<R>, accepted: (value: T, result: R) => void, failed: (error: Error) => void) {
    this.write = write;
    this.accepted = accepted;
    this.failed = failed;
    onAccountChange(() => this.cancel());
  }

  enqueue(value: T): void {
    this.pending = { id: ++this.revision, value, epoch: accountEpoch() };
    void this.drain();
  }

  cancel(): void {
    this.revision++;
    this.pending = null;
  }

  private async drain(): Promise<void> {
    if (this.active) return;
    this.active = true;
    while (this.pending) {
      const request = this.pending;
      this.pending = null;
      if (request.epoch !== accountEpoch()) continue;
      try {
        const result = await this.write(request.value);
        if (request.id === this.revision && request.epoch === accountEpoch()) this.accepted(request.value, result);
      } catch (error) {
        if (request.id === this.revision && request.epoch === accountEpoch()) this.failed(error instanceof Error ? error : new Error(String(error)));
      }
    }
    this.active = false;
  }
}
