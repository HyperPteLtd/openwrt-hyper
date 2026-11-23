let warming = "";

/**
 * Runs reads one at a time to fill the read cache for the account just signed in, and
 * stops once it signs out or changes. A failed read is left for the page that needs it.
 */
export async function warmAccount(account: string, current: () => string, reads: readonly (() => Promise<unknown>)[]): Promise<void> {
  if (!account || warming === account) return;
  warming = account;
  try {
    for (const read of reads) {
      if (current() !== account) return;
      try {
        await read();
      } catch {
        /* the page that needs it reads again and shows the error */
      }
    }
  } finally {
    if (warming === account) warming = "";
  }
}
