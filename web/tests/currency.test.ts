import assert from "node:assert/strict";
import { test } from "node:test";
import { amountScale, DEFAULT_SITE_CURRENCY, formatMinorMoney, resolveCurrency, resolveSiteCurrency } from "../src/lib/currency.ts";

test("site currency follows backend metadata with the mobile client's USD fallback", () => {
  assert.deepEqual(resolveSiteCurrency(), { unit: "USD", symbol: "$" });
  assert.deepEqual(resolveSiteCurrency({ currency: { currency_unit: " eur ", currency_symbol: " € " } }), { unit: "EUR", symbol: "€" });
  assert.deepEqual(resolveSiteCurrency({ currency: { currency_unit: " jpy ", currency_symbol: " " } }), { unit: "JPY", symbol: "JPY" });
  assert.deepEqual(resolveSiteCurrency({ currency: { currency_unit: "usdt", currency_symbol: "₮" } }), { unit: "USDT", symbol: "₮" });
  for (const unit of [undefined, null, 123, [], {}, "", " ", "US D", "<USD>", "ABCDEFGHIJKLMNOPQ"]) {
    assert.equal(resolveSiteCurrency({ currency: { currency_unit: unit } }).unit, "USD");
  }
  for (const symbol of [undefined, null, 123, [], {}, "", " "]) {
    assert.equal(resolveSiteCurrency({ currency: { currency_symbol: symbol } }).symbol, "$");
  }
});

test("account and quote amounts use fixed scale 100, even for currencies with different ISO minor digits", () => {
  assert.equal(formatMinorMoney(3600), "36.00 USD");
  assert.equal(formatMinorMoney(0), "0.00 USD");
  assert.equal(formatMinorMoney(-123456, resolveSiteCurrency({ currency: { currency_unit: "EUR" } })), "-1,234.56 EUR");
  assert.equal(formatMinorMoney(3000, resolveSiteCurrency({ currency: { currency_unit: "JPY" } })), "30.00 JPY");
  assert.equal(formatMinorMoney(123456, DEFAULT_SITE_CURRENCY, 100, "zh-CN"), "1,234.56 USD");
  for (const amount of [null, undefined, NaN, Infinity, -Infinity]) assert.equal(formatMinorMoney(amount), "--");
});

test("plan units override site metadata and scale amounts without converting currency", () => {
  const site = resolveSiteCurrency({ currency: { currency_unit: "USD", currency_symbol: "$" } });
  const units = { currency_unit: "EUR", currency_symbol: "€", amount_scale: 1000 };
  assert.deepEqual(resolveCurrency(units, site), { unit: "EUR", symbol: "€" });
  assert.equal(formatMinorMoney(2999, resolveCurrency(units, site), amountScale(units)), "2.999 EUR");
  assert.deepEqual(resolveCurrency({}, site), site);
  assert.deepEqual(resolveCurrency({ currency_unit: "HKD" }, site), { unit: "HKD", symbol: "HKD" });
  for (const scale of [0, -100, 1.5, NaN, Infinity]) {
    assert.equal(amountScale({ amount_scale: scale }), 100);
    assert.equal(formatMinorMoney(3600, site, scale), "36.00 USD");
  }
});
