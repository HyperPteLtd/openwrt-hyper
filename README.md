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
3. 在 **概览** 选择节点、测试延迟并启动服务。节点显示地区名称和旗帜，支持按地区名称或国家代码搜索。需要直连的设备可在 **接入设备** 单独设置。

界面默认跟随路由器的 LuCI 语言，支持简体中文和英文；在 **设置 → 界面语言** 可以手动选择，保存后生效。账户里的套餐名称会解析多语言字段，无限期套餐显示 `∞ 无限期`。

服务连接使用内置主地址与备用地址，并通过 `tt.cloudcdnhyper.com` 的加密 DNS TXT 记录更新备用列表，协议与 Hyper 客户端一致。地址缓存保存在路由器内存中，有效期 30 分钟；查询失败保留已有地址，5 分钟后重试。登录、验证码和不带幂等键的写请求不会跨地址重复提交。升级时旧的手动地址配置会被移除；来自非内置地址的登录凭证会清除，需要重新登录。

在 **设置 → 关于 Hyper 路由器版** 可以检查并安装更新。开启“每天自动检查更新”后，打开插件页面时每天最多检查一次，有新版本会在顶部提示，安装由管理员发起。卸载时使用 `opkg remove luci-app-hyper` 或 `apk del luci-app-hyper`。

## 开发与贡献

`web/` 是 LuCI 界面，`luci-app-hyper/` 包含 OpenWrt 软件包和路由器端服务，`hyper-crypt/` 是 Go 编写的订阅解密工具。

使用 Node.js 24 安装前端依赖并启动本地开发服务器。浏览器访问 `http://localhost:5173` 即可使用演示数据。

```sh
npm --prefix web ci
npm --prefix web run dev
```

提交前可以运行类型检查、前端测试和 Go 测试。

界面国际化统一由 `web/src/lib/i18n.ts` 管理：中文源文案作为消息键，英文词典位于 `web/src/lib/locales/en.ts`，页面通过 `t()` 翻译并使用参数插值。消息键由 TypeScript 校验，测试检查中英文参数一致性。LuCI 父页面语言优先于浏览器语言，手动选择优先于 LuCI；套餐名称、地区名称和接口的语言参数共用同一个 locale。新增界面文案应接入这一入口。

`web/src/lib/cache.ts` 统一缓存允许缓存的读取请求，并合并并发请求。账户资料缓存 1 分钟，节点和配置缓存 30 秒；可后台刷新的展示数据会先显示已有内容。公共套餐目录和支付方式缓存 10 分钟，可在当前浏览器标签页刷新后复用；账户数据只保留在页面内存中。保存设置、更新节点、购买和登录状态变化会使相关缓存失效。状态、流量、诊断、价格预览和支付结果不使用缓存；手动检查更新也会跳过缓存。

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
