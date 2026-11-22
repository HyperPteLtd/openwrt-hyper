import assert from "node:assert/strict";
import { test } from "node:test";
import { expiryText, localizedText, productText, serviceText } from "../src/lib/display.ts";

test("backend names render as text with Chinese then English fallbacks", () => {
  assert.equal(localizedText('{"en-US":"Basic","zh-CN":"Basic"}'), "Basic");
  assert.equal(localizedText('{"en-US":"Basic","zh-CN":"基础套餐"}'), "基础套餐");
  assert.equal(localizedText({ "zh-CN": " ", "en-US": "Basic" }), "Basic");
  assert.equal(localizedText({ zh_Hans: "基础套餐" }), "基础套餐");
  assert.equal(localizedText({ fr: "Forfait" }), "Forfait");
  assert.equal(localizedText("年度会员"), "年度会员");
  for (const value of [undefined, null, {}, [], "", '{"zh-CN":', { "zh-CN": 42 }]) {
    assert.equal(localizedText(value, "套餐"), "套餐");
  }
});

test("expiry distinguishes unlimited, unknown, expired and inactive plans", () => {
  const now = new Date(2026, 10, 15, 12).getTime();
  for (const value of [0, "0", null, -62135596800000]) assert.equal(expiryText(value, 1, now), "∞ 无限期");
  assert.equal(expiryText(undefined, 1, now), "到期时间未知");
  assert.equal(expiryText("invalid", 1, now), "到期时间未知");
  assert.equal(expiryText(-1, 1, now), "到期时间未知");
  assert.equal(expiryText(0, 2, now), "∞ 无限期 · 已失效");
  for (const value of [now + 86400000, (now + 86400000) / 1000, String(now + 86400000)]) {
    assert.equal(expiryText(value, 1, now), "2026-11-16 到期 · 剩余 1 天");
  }
  assert.equal(expiryText(now, 1, now), "2026-11-15 到期 · 已过期");
  assert.equal(expiryText(now + 86400000, 2, now), "2026-11-16 到期 · 已失效");
});

test("diagnostics keep useful details with product terminology", () => {
  assert.equal(serviceText("ERROR mihomo: subscription token failed"), "ERROR 代理服务: 节点配置 token failed");
  assert.equal(serviceText("[DAEMON] mihomo v1.0.0 started"), "[DAEMON] 代理服务 v1.0.0 started");
  assert.equal(serviceText("no_subscription"), "尚未开通套餐");
  assert.equal(serviceText("同步订阅失败"), "更新节点失败");
  assert.equal(serviceText("outbound/snell[test-node]: connection failed"), "outbound/连接[test-node]: connection failed");
});

test("upstream branding stays out of localized labels, notices and log output", () => {
  assert.equal(productText("PPANEL account service unavailable"), "Hyper account service unavailable");
  assert.equal(localizedText({ "zh-CN": "PPanel 基础套餐", "en-US": "PPanel Basic" }), "Hyper 基础套餐");
  assert.equal(localizedText({ "en-US": "PPanel Basic" }, "", "en-US"), "Hyper Basic");
  assert.equal(serviceText("ERROR ppanel: mihomo request failed (HTTP 503)"), "ERROR Hyper: 代理服务 request failed (HTTP 503)");
  assert.equal(productText("Hyper account service: HTTP 401"), "Hyper account service: HTTP 401");
});
