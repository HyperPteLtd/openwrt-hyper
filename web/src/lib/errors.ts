import { serviceText } from "./display.ts";
import { t as tr, type MessageKey } from "./i18n.ts";

/**
 * The page's words for every error code hyperd reports (and the few the page makes up
 * itself). tests/errors.test.ts reads the router's Go sources and fails when a code it
 * emits has no text here.
 */
export const errorTexts: Record<string, MessageKey> = {
  // A start or restart (status.operation.error); hyperd's log has the cause.
  start_failed: "代理服务没有启动成功",
  core_start_failed: "代理服务没有按时就绪",
  core_config_invalid: "生成的代理配置没有通过检查",
  core_group_missing: "路由器上缺少 Hyper 的运行组，请重新安装插件",
  core_exited_on_start: "代理服务启动后立即退出",
  core_unresponsive_on_start: "代理服务启动后没有响应",
  takeover_lost_on_start: "接管网络后规则随即被移除，可能是防火墙重新加载了",
  network_takeover_failed: "接管网络流量没有成功",
  no_nodes: "还没有节点，请先更新节点",
  timeout: "操作超时",
  // Software updates.
  update_failed: "更新没有成功",
  update_in_progress: "正在安装更新",
  automatic_update_disabled: "自动更新已关闭",
  up_to_date: "已经是最新版本",
  install_failed: "安装没有成功",
  update_repo_invalid: "更新源地址无效",
  update_no_package: "这个版本没有适合这台路由器的安装包",
  update_checksum_failed: "安装包校验没有通过，已放弃安装",
  update_rate_limited: "GitHub 暂时限制了访问次数，请稍后再试",
  update_release_missing: "没有找到已发布的版本",
  update_server_error: "GitHub 暂时无法提供更新",
  update_unreachable: "无法连接 GitHub，请检查网络",
  // Account and plans.
  "not signed in": "请先登录账户",
  not_signed_in: "请先登录账户",
  "missing credentials": "请填写完整的登录信息",
  "illegal email": "邮箱地址格式不正确",
  "illegal date": "这台电脑和路由器的日期相差太多，请先校准时间",
  "write failed": "路由器存储写入失败，请稍后再试",
  "path not allowed": "这个操作在路由器上不可用",
  "idempotency key required": "请求缺少标识，请刷新页面后重试",
  api_unavailable: "暂时无法连接服务，请检查网络后重试",
  session_changed: "账户已变更，请刷新页面",
  session_write_failed: "保存登录状态失败，请稍后再试",
  login_failed: "登录失败",
  register_failed: "注册失败",
  invalid_json: "服务返回了无法识别的内容",
  invalid_body: "服务返回了无法识别的内容",
  no_subscription: "尚未开通套餐",
  subscribe_list_failed: "读取套餐信息失败",
  service_unavailable: "Hyper 服务没有运行",
  // Updating nodes.
  subscription_token: "获取节点访问凭证失败",
  locations: "读取节点地区失败",
  no_countries: "当前套餐没有可用的节点地区",
  nodes: "没有读取到可用的节点",
  nodes_write_failed: "保存节点失败",
  decrypt: "节点数据解密失败",
  // Nodes, latency tests and selection.
  "unknown node": "节点不存在，请刷新节点列表",
  "invalid proxy name": "节点名称无效",
  "delay test failed": "节点测速失败",
  "core controller unavailable": "代理服务没有响应",
  invalid_job: "测速任务无效",
  probe_config_failed: "无法生成测速配置",
  probe_start_failed: "无法开始测速",
  probe_failed: "测速失败",
  probe_timeout: "测速超时",
  proxy_not_core: "下载用的代理不是 Hyper 的代理服务",
  // Settings, diagnostics and requests.
  "illegal mode": "不支持的模式",
  "illegal values": "设置内容无效",
  "illegal key": "不支持的设置项",
  "illegal target": "目标只能是域名或 IP 地址",
  "illegal type": "不支持的类型",
  "illegal method": "不支持的操作",
  "illegal task": "任务不存在或已过期",
  "request too large": "请求内容过大",
  internal_error: "路由器内部错误，请查看日志",
  task_failed: "操作没有成功，请查看日志",
};

/**
 * Text for an error hyperd or a request reported: a code reads in the page's language,
 * anything else keeps its details in product terms. Empty without an error.
 */
export function errorText(raw: unknown, fallback = ""): string {
  const code = typeof raw === "string" ? raw.trim() : "";
  if (!code) return fallback;
  const known = Object.hasOwn(errorTexts, code) ? errorTexts[code] : undefined;
  if (known) return tr(known);
  const entry = code.match(/^illegal entry: ([\s\S]*)$/);
  if (entry) return tr("名单里有无法识别的条目：{0}", [entry[1]]);
  const value = code.match(/^illegal value for (.+)$/);
  if (value) return tr("设置值无效：{0}", [value[1]]);
  return serviceText(code);
}
