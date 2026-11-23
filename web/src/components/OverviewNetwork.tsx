import { t as tr } from "../lib/i18n";
import { Devices } from "../pages/Devices";
import { Routing } from "../pages/Routing";
import { Card, CardHeader } from "./ui";
import "./OverviewNetwork.css";

/** Network controls are edited directly on the overview with shared autosave. */
export function OverviewNetwork({ section }: { section: "rules" | "custom" | "dns" | "devices" }) {
  return (
    <Card className={`network-section network-${section}`}>
      {section !== "devices" && <CardHeader
        title={section === "rules" ? tr("分流规则") : section === "custom" ? tr("我的规则") : section === "dns" ? "DNS" : tr("接入设备")}
        desc={section === "custom" ? tr("每行一个域名或关键词，比服务端规则优先") : undefined}
      />}
      {section === "devices" ? <Devices embedded /> : <Routing section={section} embedded />}
    </Card>
  );
}
