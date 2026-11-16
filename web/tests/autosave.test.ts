import assert from "node:assert/strict";
import { invalidateAccountWork } from "../src/lib/account-epoch.ts";
import { test } from "node:test";
import { AutosaveQueue, invalidDomainLine, validDnsAddress } from "../src/lib/autosave.ts";

function deferred() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

const turn = () => new Promise<void>((resolve) => setTimeout(resolve, 0));
const merge = (older: Record<string, string>, newer: Record<string, string>) => ({ ...older, ...newer });

test("account changes discard debounced fields and release flush without saving them", async () => {
  const writes: string[] = [];
  const queue = new AutosaveQueue<string>(async value => { writes.push(value); });
  queue.enqueue("old-account", 25);
  invalidateAccountWork();
  await queue.flush();
  await new Promise(resolve => setTimeout(resolve, 30));
  assert.deepEqual(writes, []);
  assert.equal(queue.busy, false);
  assert.equal(queue.value, null);
  assert.equal(queue.error, null);
  assert.equal(queue.savedRevision, 0);
});

test("old account failures do not restore drafts or block the next account's save", async () => {
  const first = deferred();
  const writes: Record<string, string>[] = [];
  const queue = new AutosaveQueue<Record<string, string>>(value => {
    writes.push(value); return writes.length === 1 ? first.promise : Promise.resolve();
  }, merge);
  queue.enqueue({ oldField: "sent" }); queue.enqueue({ oldPending: "discard" });
  invalidateAccountWork();
  assert.equal(queue.busy, false);
  await queue.flush(); // does not wait for a request that belongs to another account
  queue.enqueue({ newField: "keep" });
  const completed = queue.flush();
  first.reject(Error("old account failed"));
  await completed;
  assert.deepEqual(writes, [{ oldField: "sent" }, { newField: "keep" }]);
  assert.equal(queue.error, null);
  assert.equal(queue.savedRevision, 1);
});

test("old account successes cannot show saved after cancellation", async () => {
  const first = deferred();
  const queue = new AutosaveQueue<string>(() => first.promise);
  queue.enqueue("old");
  invalidateAccountWork();
  first.resolve(); await turn();
  assert.equal(queue.busy, false);
  assert.equal(queue.savedRevision, 0);
  assert.equal(queue.error, null);
});

test("rapid edits serialize and coalesce without an older completion overwriting the latest value", async () => {
  const requests: { values: Record<string, string>; request: ReturnType<typeof deferred> }[] = [];
  const queue = new AutosaveQueue<Record<string, string>>((values) => {
    const request = deferred();
    requests.push({ values, request });
    return request.promise;
  }, merge);
  queue.enqueue({ mode: "first" });
  queue.enqueue({ mode: "second" });
  queue.enqueue({ mode: "latest", ipv6: "1" });
  assert.equal(requests.length, 1);
  assert.deepEqual(queue.value, { mode: "latest", ipv6: "1" });
  let finished = false;
  const flush = queue.flush().then(() => { finished = true; });
  requests[0].request.resolve();
  await turn();
  assert.equal(finished, false);
  assert.deepEqual(requests.map((request) => request.values), [{ mode: "first" }, { mode: "latest", ipv6: "1" }]);
  requests[1].request.resolve();
  await flush;
  assert.equal(queue.busy, false);
  assert.equal(queue.error, null);
});

test("leaving a page flushes pending text immediately and waits for its write", async () => {
  const request = deferred();
  const writes: string[] = [];
  const queue = new AutosaveQueue<string>((value) => { writes.push(value); return request.promise; });
  queue.enqueue("example", 10000);
  queue.enqueue("example.com", 10000);
  assert.deepEqual(writes, []);
  const flushed = queue.flush();
  assert.deepEqual(writes, ["example.com"]);
  assert.equal(queue.busy, true);
  request.resolve();
  await flushed;
  assert.equal(queue.value, null);
});

test("a newer edit proceeds after an obsolete failure and retains unrelated failed fields", async () => {
  const first = deferred();
  const writes: Record<string, string>[] = [];
  const queue = new AutosaveQueue<Record<string, string>>(async (value) => {
    writes.push(value);
    if (writes.length === 1) await first.promise;
  }, merge);
  queue.enqueue({ dns: "first.example", ipv6: "1" });
  queue.enqueue({ dns: "latest.example" });
  first.reject(new Error("router unreachable"));
  await queue.flush();
  assert.equal(writes.length, 2);
  assert.deepEqual(writes[1], { dns: "latest.example", ipv6: "1" });
  assert.equal(queue.busy, false);
  assert.equal(queue.error, null);
});

test("a failure of the latest edit remains visible until explicit retry", async () => {
  const first = deferred(), latest = deferred();
  let calls = 0;
  const queue = new AutosaveQueue<string>(() => ++calls === 1 ? first.promise : calls === 2 ? latest.promise : Promise.resolve());
  queue.enqueue("old");
  queue.enqueue("latest");
  first.reject(Error("obsolete failure"));
  await turn();
  assert.equal(queue.error, null);
  latest.reject(Error("latest failure"));
  await assert.rejects(queue.flush(), /latest failure/);
  assert.equal(queue.value, "latest");
  await queue.retry();
  assert.equal(queue.error, null);
});

test("a new edit after failure can retry without dropping an unrelated failed field", async () => {
  let fail = true;
  const writes: Record<string, string>[] = [];
  const queue = new AutosaveQueue<Record<string, string>>(async (value) => {
    writes.push(value);
    if (fail) throw new Error("failed");
  }, merge);
  queue.enqueue({ ipv6: "1" });
  await assert.rejects(queue.flush());
  fail = false;
  queue.enqueue({ mode: "tun" });
  await queue.flush();
  assert.deepEqual(writes[1], { ipv6: "1", mode: "tun" });
});

test("invalid text cancels its earlier debounced write without cancelling other settings", async () => {
  const writes: Record<string, string>[] = [];
  const queue = new AutosaveQueue<Record<string, string>>(async (value) => { writes.push(value); }, merge);
  queue.enqueue({ dns: "dns.google", ipv6: "1" }, 10000);
  queue.discardPending(({ dns: _, ...rest }) => Object.keys(rest).length ? rest : null);
  await queue.flush();
  assert.deepEqual(writes, [{ ipv6: "1" }]);
  queue.enqueue({ dns: "dns.google" }, 10000);
  queue.discardPending(() => null);
  await queue.flush();
  assert.equal(writes.length, 1);
  assert.equal(queue.busy, false);
});

test("saved status counts acknowledgments, never initial reads, cancelled input, or failed writes", async () => {
  const first = deferred();
  let writes = 0;
  const queue = new AutosaveQueue<string>(async () => {
    if (++writes === 1) await first.promise;
  });
  assert.equal(queue.savedRevision, 0);
  queue.enqueue("cancelled", 10000);
  queue.discardPending(() => null);
  await queue.flush();
  assert.equal(queue.savedRevision, 0);
  assert.equal(writes, 0);
  queue.enqueue("retry me");
  assert.equal(queue.savedRevision, 0);
  first.reject(Error("offline"));
  await assert.rejects(queue.flush(), /offline/);
  assert.equal(queue.savedRevision, 0);
  await queue.retry();
  assert.equal(queue.savedRevision, 1);
  queue.enqueue("cancelled again", 10000);
  queue.discardPending(() => null);
  await queue.flush();
  assert.equal(queue.savedRevision, 1);
});

test("automatic DNS writes require complete supported addresses", () => {
  for (const value of ["wan", "1.1.1.1", "223.5.5.5", "tls://dns.google", "https://1.1.1.1/dns-query", "https://dns.google/dns-query", "udp://1.1.1.1:53", "2001:4860:4860::8888", "[2001:4860:4860::8888]:53"]) {
    assert.equal(validDnsAddress(value), true, value);
  }
  for (const value of ["", "https://", "https:/", "1.1", "1.1.1", "1.1.1.256", "udp://1.1:53", "https://-dns.example/", "dns.", "dns", "1.1.1.1:65536", "https://dns.google/user name", "https://user:pass@dns.google", "file:///tmp/config"]) {
    assert.equal(validDnsAddress(value), false, value);
  }
});

test("domain lists accept empty lists, keywords and CRLF and reject a malformed complete line", () => {
  assert.equal(invalidDomainLine(""), undefined);
  assert.equal(invalidDomainLine("company.com\r\nlocal-bank\rinternal_site\n"), undefined);
  assert.equal(invalidDomainLine("company.com\nhttps://example.com"), "https://example.com");
  assert.equal(invalidDomainLine("company.com\nwrong domain"), "wrong domain");
});
