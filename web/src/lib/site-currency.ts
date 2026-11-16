import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import { app } from "./app";
import { api, type ApiResult } from "./api";
import { DEFAULT_SITE_CURRENCY, resolveSiteCurrency, type SiteConfig } from "./currency";

/** Both pages share the RPC read cache, including persisted currency-only data. */
export function useSiteCurrency() {
  const currency = useSignal(DEFAULT_SITE_CURRENCY);
  const tick = app.tick;
  useEffect(() => {
    let live = true;
    const apply = (result: ApiResult<SiteConfig>) => {
      if (live && result.ok) currency.value = resolveSiteCurrency(result.data);
    };
    api<SiteConfig>("GET", "/v1/common/site/config", { onUpdate: apply }).then(apply).catch(() => {
      // Keep the last known currency offline, or the mobile client's USD default.
    });
    return () => { live = false; };
  }, [tick]);
  return currency.value;
}
