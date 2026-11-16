# Hyper for OpenWrt

[![CI](https://github.com/HyperPteLtd/openwrt-hyper/actions/workflows/ci.yml/badge.svg)](https://github.com/HyperPteLtd/openwrt-hyper/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/HyperPteLtd/openwrt-hyper)](https://github.com/HyperPteLtd/openwrt-hyper/releases/latest)

Hyper for OpenWrt 是一个 LuCI 插件。用 Hyper 账户登录路由器后，可以集中管理局域网设备的代理与 DNS，手机、电脑和游戏机可直接使用路由器的网络配置。

## 功能

- 使用邮箱验证码登录，自动同步 Hyper 节点与分流规则。
- 支持智能分流、全局代理和仅 DNS 保护，可指定局域网设备直连。
- 支持加密 DNS、自定义直连与代理域名、节点测速和网络诊断。
- 在 LuCI 中查看账户与订阅、接入设备、实时流量和运行日志。
- 从 GitHub Releases 检查更新，下载后校验软件包的 SHA-256。

## 兼容性

| OpenWrt | 包管理器 | Release 附件 |
| --- | --- | --- |
| 23.05、24.10 | opkg | `23.05-24.10_luci-app-hyper_<版本>_<架构>.ipk` |
| 25.12 及以后 | apk | `25.12+_luci-app-hyper-<版本>-<架构>.apk` |

发布包提供 `aarch64_cortex-a53`、`aarch64_cortex-a72`、`aarch64_generic`、`arm_cortex-a7_neon-vfpv4`、`arm_cortex-a9_vfpv3-d16`、`arm_cortex-a15_neon-vfpv4`、`i386_pentium4`、`loongarch64_generic`、`mipsel_24kc`、`riscv64_riscv64`（23.05、24.10）/ `riscv64_generic`（25.12+）和 `x86_64`。用以下命令确认路由器的固件版本和软件包架构。

```sh
cat /etc/openwrt_release
```

安装需要 LuCI、firewall4 / nftables，以及可用的 OpenWrt 软件源。建议至少预留 100 MB 闪存并使用 256 MB 或更多内存。每个安装包都包含 sing-box 和订阅解密工具。

## 安装

1. 按固件版本和 `DISTRIB_ARCH` 下载一个安装包，将它上传到路由器的 `/tmp`。可用 `scp -O` 兼容 OpenWrt 的 Dropbear SSH。 [下载最新版本](https://github.com/HyperPteLtd/openwrt-hyper/releases/latest)
2. SSH 登录路由器，运行对应命令。25.12 的 APK 未签名，因此安装时需要 `--allow-untrusted`。

**OpenWrt 23.05 / 24.10**

```sh
opkg update
opkg install /tmp/23.05-24.10_luci-app-hyper_*.ipk
```

**OpenWrt 25.12+**

```sh
apk update
apk add --allow-untrusted /tmp/25.12+_luci-app-hyper-*.apk
```

## 首次使用

1. 在 LuCI 打开 **服务 → Hyper**。
2. 用邮箱验证码登录，等待节点同步。插件自动获取 Hyper 服务地址，无需手动填写。
3. 在 **概览** 选择节点、测试延迟并启动服务。节点显示地区名称和旗帜，支持按地区名称或国家代码搜索。首页直接展示我的规则、分流规则、DNS 和接入设备，需要直连的设备可逐台设置。

界面默认跟随路由器的 LuCI 语言，支持简体中文和英文；在 **设置 → 界面语言** 可以手动选择，选择后立即生效。账户里的套餐名称会解析多语言字段，无限期套餐显示 `∞ 无限期`。

服务连接使用内置主地址与备用地址，并通过 `tt.cloudcdnhyper.com` 的加密 DNS TXT 记录更新备用列表，协议与 Hyper 客户端一致。地址缓存保存在路由器内存中，有效期 30 分钟；查询失败保留已有地址，5 分钟后重试。登录、验证码和不带幂等键的写请求不会跨地址重复提交。升级时旧的手动地址配置会被移除；来自非内置地址的登录凭证会清除，需要重新登录。

在 **设置 → 软件更新** 可以检查并安装更新。“自动检查新版本”每天检查并提示；“自动更新”默认关闭，开启后每天检查并安装适合当前固件与架构的新版本。两个开关独立工作，关闭页面或代理服务后仍会执行后台计划。卸载时使用 `opkg remove luci-app-hyper` 或 `apk del luci-app-hyper`。

顶部导航分为概览、账户与订阅、诊断与日志、设置。公告位于首页顶部，邀请信息位于账户页，签到从头部按钮打开。网络诊断和合并后的服务、连接日志在同页操作，日志一条一行并按级别着色，支持级别筛选。账户与套餐金额跟随后台配置的币种，不在前端自行换汇。

配置修改后自动保存并应用，文本输入经过校验与防抖；导航右侧统一显示保存状态，失败时保留修改并支持重试。切换页面会继续提交有效修改，无需额外保存或应用。

## 开发与贡献

`web/` 是 LuCI 界面，`luci-app-hyper/` 包含 OpenWrt 软件包和路由器端服务，`hyper-crypt/` 是 Go 编写的订阅解密工具。

使用 Node.js 24 安装前端依赖并启动本地开发服务器。浏览器访问 `http://localhost:5173` 即可使用演示数据。

```sh
npm --prefix web ci
npm --prefix web run dev
```

提交前可以运行类型检查、前端测试和 Go 测试。

界面国际化统一由 `web/src/lib/i18n.ts` 管理：中文源文案作为消息键，英文词典位于 `web/src/lib/locales/en.ts`，页面通过 `t()` 翻译并使用参数插值。消息键由 TypeScript 校验，测试检查中英文参数一致性。LuCI 父页面语言优先于浏览器语言，手动选择优先于 LuCI；套餐名称、地区名称和接口的语言参数共用同一个 locale。新增界面文案应接入这一入口。

`web/src/lib/cache.ts` 统一缓存允许缓存的读取请求，并合并并发请求。账户资料缓存 1 分钟，节点和配置缓存 30 秒；账户、订阅、节点、接入设备、公告和公共套餐目录的展示数据按路由器与账户隔离，持久保存最多 7 天。重开页面先显示已有内容，再后台核对变化。浏览器不会保存登录凭据、订阅链接或节点连接配置；退出登录或切换账户会清理旧缓存。设置保存导致的读取失效会统一重新获取，同一账户的页面不会因并发保存停在加载状态。运行状态、流量、诊断、价格预览和支付结果不使用缓存，服务控制等待首次真实状态返回；手动检查更新也会跳过缓存。

节点延迟与测量时间同样按账户和节点保存，切页或重开后可直接查看上次结果。主动刷新始终重新测速，失败时保留上次成功值；自动选择仅使用本次测速结果。后台更换节点身份、移除节点或退出账户时会清理对应记录。

内核适配参考 hyper-mobile：使用显式 HTTP 下载客户端、新 DNS 回答筛选方式和默认 TUN 协议栈，开启 DNS 缓存，并通过 `initial_path` 从随包规则启动。规则保持路由器原有数据源，固定提交和 SHA-256 位于 `luci-app-hyper/root/etc/hyper/rule-set/manifest.json`；`python3 scripts/bundled-rules.py` 校验，`--fetch` 按清单恢复。路由器继续生成适配 dnsmasq / nftables 的配置，没有直接使用移动端的整份 TUN 配置。

启停 RPC 只提交操作，后台 worker 串行执行，状态接口返回进度和结果；启动要等本地控制 API 就绪才算完成。前端立即反馈操作状态，运行时长按秒显示并与真实进程时长校准。CI 除配置校验外还会实际启动固定版本的核心，覆盖无缓存离线启动、DNS 分流和控制请求的响应速度。

```sh
npm --prefix web run typecheck
npm --prefix web test
(cd hyper-crypt && go test ./...)
```

本地打包需要能运行 Linux x86_64 容器的 Docker。以下命令使用 OpenWrt SDK 为目标平台打包，产物位于 `dist/`。

```sh
sh scripts/build-packages.sh mediatek/filogic
```

欢迎通过 Issue 报告问题或提交 Pull Request。报告问题时请附上 OpenWrt 版本、架构、插件版本和复现步骤，并遮盖账号、令牌及订阅链接。 [提交 Issue](https://github.com/HyperPteLtd/openwrt-hyper/issues)

### 自动发布

`main` 上的提交按 Conventional Commits 决定下一版本。首次发布使用 `luci-app-hyper/Makefile` 中的版本，之后从最近的正式标签递增。

| 提交类型 | 版本变化 |
| --- | --- |
| `fix:`、`perf:`、`refactor:` | Patch |
| `feat:` | Minor |
| `BREAKING CHANGE:` 或 `feat!:` 等破坏性变更标记 | Major |
| `docs:`、`test:`、`ci:`、`chore:` | 仅运行 CI |

CI 为每种架构构建 IPK 和 APK，并在 OpenWrt 23.05、24.10 和 25.12 的 x86_64 与 aarch64 用户空间安装测试。通过后创建标签和 GitHub Release，附上软件包及 `sha256sums`。维护者也可以推送 `vX.Y.Z` 标签来指定版本。 [查看 CI](https://github.com/HyperPteLtd/openwrt-hyper/actions/workflows/ci.yml)

## 许可证

LuCI 插件源自 HomeProxy，采用 GPL-2.0-only。安装包内独立分发的 sing-box 来自 libbox，采用 GPL-3.0-or-later。 [项目许可证](LICENSE) · [HomeProxy 源码](https://github.com/immortalwrt/homeproxy) · [libbox 源码](https://github.com/HyperPteLtd/libbox)
