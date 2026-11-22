import assert from "node:assert/strict";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { errorText, errorTexts } from "../src/lib/errors.ts";

const repo = fileURLToPath(new URL("../..", import.meta.url));

function goSources(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return goSources(path);
    return name.endsWith(".go") && !name.endsWith("_test.go") ? [path] : [];
  });
}

// The forms in which hyperd hands the page an error code.
const emitted = [
  /rpcError\{"([^"]+)"\}/g, // an RPC's {"error": code}
  /coded\("([^"]+)"/g, // a coded failure (a start's operation error)
  /"error":\s*"([^"]+)"/g, // a reply map's error
  /\bfail\("([^"]+)"/g, // a reply or sync failure
  /\bError(?::|\s*=)\s*"([^"]+)"/g, // a Result's or state's Error
  /[^!=]=\s*"failed",\s*"([^"]+)"/g, // an operation or job set to failed with its error
  /errText = "([^"]+)"/g,
  /return "((?:start|update)_[a-z_]+)"/g, // errCode, updateCode
  /(?:errors\.New|fmt\.Errorf)\("([a-z]+(?:_[a-z]+)+)"\)/g, // a snake-case error
];

test("every error code hyperd emits reads in the page's words", () => {
  const codes = new Set<string>();
  for (const file of [...goSources(join(repo, "cmd")), ...goSources(join(repo, "internal"))]) {
    const source = readFileSync(file, "utf8");
    for (const form of emitted) for (const m of source.matchAll(form)) codes.add(m[1]);
  }
  // Not errors: an operation's state and the core's own setting values.
  for (const word of ["failed", "ERROR", "routing-mark", "silent"]) codes.delete(word);
  assert.ok(codes.size > 40, `only ${codes.size} codes found: the patterns no longer match hyperd`);
  const missing = [...codes].filter((code) => {
    if (Object.hasOwn(errorTexts, code)) return false;
    const text = errorText(code.endsWith(" ") || code.endsWith(": ") ? `${code}x` : code);
    return text === code || text === `${code}x`;
  });
  assert.deepEqual(missing, []);
});

test("codes the page itself reports have texts too", () => {
  for (const code of ["probe_start_failed", "probe_failed", "task_failed", "timeout"]) assert.ok(Object.hasOwn(errorTexts, code), code);
});

test("error text keeps details it cannot name", () => {
  assert.equal(errorText(""), "");
  assert.equal(errorText(undefined, "失败"), "失败");
  assert.equal(errorText("core_exited_on_start"), "代理服务启动后立即退出");
  assert.equal(errorText("illegal entry: *.bad"), "名单里有无法识别的条目：*.bad");
  assert.equal(errorText("illegal value for config.mode"), "设置值无效：config.mode");
  assert.equal(errorText("toString"), "toString");
  // Free text keeps its words, in product terms.
  assert.equal(errorText("mihomo exited"), "代理服务 exited");
});
