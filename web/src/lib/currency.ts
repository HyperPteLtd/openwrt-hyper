export interface CurrencyDisplay { unit: string; symbol: string }
export interface CurrencyConfig { currency_unit?: string; currency_symbol?: string }
export interface SiteConfig { currency?: CurrencyConfig }
export interface AmountUnits extends CurrencyConfig { amount_scale?: number }

export const DEFAULT_SITE_CURRENCY: CurrencyDisplay = { unit: "USD", symbol: "$" };
export const MINOR_AMOUNT_SCALE = 100;

function record(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : {};
}

/** Backend currency codes are uppercase letters/digits, at most 16 characters. */
export function resolveCurrency(value: unknown, fallback = DEFAULT_SITE_CURRENCY): CurrencyDisplay {
  const source = record(value);
  const rawUnit = typeof source.currency_unit === "string" ? source.currency_unit.trim().toUpperCase() : "";
  const unit = /^[A-Z0-9]{1,16}$/.test(rawUnit) ? rawUnit : fallback.unit;
  const rawSymbol = typeof source.currency_symbol === "string" ? source.currency_symbol.trim() : "";
  const symbol = rawSymbol && rawSymbol.length <= 16 ? rawSymbol : unit === fallback.unit ? fallback.symbol : unit;
  return { unit, symbol };
}

export function resolveSiteCurrency(config?: unknown): CurrencyDisplay {
  return resolveCurrency(record(config).currency);
}

export function amountScale(units?: AmountUnits): number {
  const scale = units?.amount_scale;
  return typeof scale === "number" && Number.isSafeInteger(scale) && scale > 0 ? scale : MINOR_AMOUNT_SCALE;
}

/** Match mobile: show the backend currency code, without currency conversion. */
export function formatMinorMoney(amount: number | null | undefined, currency = DEFAULT_SITE_CURRENCY, scale = MINOR_AMOUNT_SCALE, locale = "en-US"): string {
  if (amount == null || !Number.isFinite(amount)) return "--";
  const divisor = amountScale({ amount_scale: scale });
  const digits = Math.max(2, Math.ceil(Math.log10(divisor)));
  const value = new Intl.NumberFormat(locale, { minimumFractionDigits: 2, maximumFractionDigits: digits }).format(amount / divisor);
  return `${value} ${currency.unit}`;
}
