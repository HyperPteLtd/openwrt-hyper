import assert from "node:assert/strict";
import { test } from "node:test";
import { warmAccount } from "../src/lib/warm.ts";

test("reads run one at a time, past failures, for the account signed in", async () => {
  const log: string[] = [];
  let open = 0;
  const read = (name: string, fail = false) => async () => {
    assert.equal(open++, 0, "two reads at once");
    log.push(name);
    await new Promise((resolve) => setTimeout(resolve, 1));
    open--;
    if (fail) throw new Error(name);
  };
  await warmAccount("a@b.c", () => "a@b.c", [read("info"), read("subscribe", true), read("plans")]);
  assert.deepEqual(log, ["info", "subscribe", "plans"]);
});

test("signing out or switching accounts stops the rest", async () => {
  let account = "a@b.c";
  const log: string[] = [];
  await warmAccount("a@b.c", () => account, [
    async () => { log.push("info"); account = ""; },
    async () => { log.push("subscribe"); },
  ]);
  assert.deepEqual(log, ["info"]);
});

test("a second start for the same account while one runs does nothing", async () => {
  let release!: () => void;
  let runs = 0;
  const first = warmAccount("a@b.c", () => "a@b.c", [() => { runs++; return new Promise<void>((resolve) => { release = resolve; }); }]);
  await warmAccount("a@b.c", () => "a@b.c", [async () => { runs++; }]);
  release();
  await first;
  assert.equal(runs, 1);
  await warmAccount("a@b.c", () => "a@b.c", [async () => { runs++; }]);
  assert.equal(runs, 2, "a later sign-in warms again");
});
