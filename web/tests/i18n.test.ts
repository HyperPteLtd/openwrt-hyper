import assert from "node:assert/strict";
import { test } from "node:test";
import { resolveLocale, t } from "../src/lib/i18n.ts";
import { en } from "../src/lib/locales/en.ts";
import { localizedText } from "../src/lib/display.ts";

test("router language takes priority over browser language unless explicitly overridden", () => {
  assert.equal(resolveLocale("auto", "zh-cn", "en-US"), "zh-CN");
  assert.equal(resolveLocale("auto", "en", "zh-CN"), "en-US");
  assert.equal(resolveLocale("auto", "zh_Hans", "en-US"), "zh-CN");
  assert.equal(resolveLocale("en-US", "zh-cn", "zh-CN"), "en-US");
  assert.equal(resolveLocale("zh-CN", "en", "en-US"), "zh-CN");
  assert.equal(resolveLocale("auto", "", "en-US"), "en-US");
});

test("all translations preserve their interpolation arguments", () => {
  const placeholders = (s: string) => [...s.matchAll(/\{\d+\}/g)].map(([key]) => key).sort();
  for (const [key, translation] of Object.entries(en)) {
    assert.deepEqual(placeholders(translation), placeholders(key), key);
    assert(translation.trim(), key);
  }
  assert.equal(t("已保存 {0} 条", [3]), "已保存 3 条");
  assert.equal(t("保存失败"), "保存失败");
});

test("API labels follow the same selected language as the interface", () => {
  const name = '{"en-US":"Basic","zh-CN":"基础套餐"}';
  assert.equal(localizedText(name, "", "zh-CN"), "基础套餐");
  assert.equal(localizedText(name, "", "en-US"), "Basic");
});
