# 开发指南

[返回 README](../README.md)

## 代码结构

| 目录 | 内容 |
| --- | --- |
| `cmd/hyperd/` | 路由器管理进程：rpcd 对象 `hyper`、启停与接管、后台任务、定时同步与更新、0.x 设置迁移 |
| `cmd/hyper-kernel`、`hyper-net`、`hyper-subscription` | 由 hyperd 调用的子命令：内核安装与校验、网络辅助、账户与订阅 |
| `internal/` | 各模块：`router`（配置生成与接管）、`kernel`（内核存储与事务）、`subscription` 与 `hypersync`（Hyper 账户订阅）、`account`、`links`（分享链接转换）、`release`（发布命名） |
| `files/` | 装入软件包的文件：init 脚本、网络接管脚本、rpcd 插件与 ACL、LuCI 页面（`www/luci-static/resources/hyper/`）、随包规则集 |
| `packaging/openwrt/` | SDK 打包、安装钩子、插件自更新、内核获取与安装测试 |
| `scripts/` | 内核锁定文件 `core.lock`、LuCI 检查、发布版本脚本、真机/容器验证工具 |

所有命令从仓库根目录执行。Go 版本见 `go.mod`；hyperd 只面向 Linux，Go 测试需在 Linux 主机、虚拟机或容器中运行

## 测试

```sh
go test -race ./...
node scripts/check-luci.mjs
```

`check-luci.mjs` 按浏览器加载的方式检查 LuCI 页面：`view/` 下为 LuCI 求值的函数体，其余为 ES 模块，并确认相对导入存在。页面就是 LuCI 原生 JavaScript，没有构建步骤。它同时检查英文：页面上的每段文字都写成 `t('中文')`（带参数时 `t('当前 {0}', v)`），[en.js](../files/www/luci-static/resources/hyper/en.js) 以中文为键给出英文；缺英文、多余的键或 `t()` 之外的中文都会失败（注释和正则表达式除外）。语言由 [i18n.js](../files/www/luci-static/resources/hyper/i18n.js) 决定：默认跟随 LuCI 的语言，在概览的“界面语言”中按浏览器保存选择，切换后重新载入页面；账户接口的服务端文字按同一语言请求。`cmd/hyperd` 的 `TestEmittedCodesReadInChinese` 读取 Go 源码，hyperd 报出的错误码或任务名在页面没有中文时失败

CI 还以 root 在独立网络命名空间里运行随包内核（`internal/control`、`internal/router` 的真实内核测试），用 shellcheck 检查路由器脚本，并在官方 OpenWrt rootfs 容器中安装软件包（[install-test.sh](../packaging/openwrt/install-test.sh)）：离线安装、LuCI 与 rpcd 调用、daemon 安装随包内核、插件自更新的各项校验、从最新正式版升级以及卸载

## 路由器运行结构

procd 运行 `hyperd serve` 与 `hyperd guard`。rpcd 插件 `/usr/libexec/rpcd/hyper` 把调用交给 serve。设置在 `/etc/config/hyper` 的 `hyper.main`，设备标识在 `hyper.account.device_id`；路由意图（模式、设备与自定义规则）在 `/etc/hyper/private/intent.json`，登录会话在 `/etc/hyper/session`，订阅在 `/etc/hyper/sources`

- 账户订阅由 `hyper-subscription` 读取：令牌、地区、按地区加密的节点和分流规则目录，转换为一个“Hyper”选择组。同步按间隔或每天定时（`update_hours`、`update_at`）
- 网络接管由 `files/usr/libexec/hyper/network` 完成，TProxy 或 TUN 按防火墙自动选择；仅 DNS 保护（`dns_only`）只接管 DNS
- `router_self` 决定路由器自身流量是否经过内核；关闭时 DNS 保护返回真实地址
- 插件每天检查一次新版本（`auto_check`），`auto_update` 开启时自动安装。安装在独立会话中运行，输出写入 `/var/run/hyper/plugin-update.*`，服务重启不会中断包管理器

### 从 0.x 升级

0.x 的软件包依赖与本版本相同，因此 0.x 离线更新无需新依赖。新包的 preinst 停止 0.x 服务并释放它的接管，路由器既放不下内核时拒绝安装。`/etc/config/hyper` 是 conffile，升级后仍是 0.x 的分区；hyperd 首次启动时（[legacy.go](../cmd/hyperd/legacy.go)）把它们迁移为 `hyper.main` 和路由意图，自定义关键词名单变为自定义规则，关闭的分流规则在登录状态下交给账户订阅，然后删除 0.x 的文件。会话和设备标识不变

## 内核

每个软件包携带本 CPU 的内核，来源与各架构的 SHA-256 固定在 [core.lock](../scripts/core.lock)。[fetch-core.sh](../packaging/openwrt/fetch-core.sh) 下载并校验发布的原始构建，以 `CORE_TAG` 命名，写出 `pin.json`，连同来源说明装入 `/usr/share/hyper/core` 与许可证目录

路由器上 `hyper-kernel` 依据 `pin.json` 离线安装：首次启动、升级带来新版本时由 hyperd 安装并在校验配置后选用。没有选择存储位置时优先放闪存，空间不足时放内存，重启后从插件重新解压。此版本不提供在线内核下载

更新内核：修改 `core.lock` 中的 `CORE_RELEASE`、`CORE_TAG`，以及该发布 sha256sums 中每个架构的 `CORE_SHA256` 和 `CORE_NOTICES` 行

## 支持的架构

每个正式版为两种固件格式各提供 14 个包架构：

| 架构类别 | 包架构 |
| --- | --- |
| ARM64 | `aarch64_cortex-a53`、`aarch64_cortex-a72`、`aarch64_generic` |
| ARMv7 | `arm_cortex-a7_neon-vfpv4`、`arm_cortex-a9_vfpv3-d16`、`arm_cortex-a15_neon-vfpv4` |
| ARMv5 / v6 | `arm_arm926ej-s`、`arm_arm1176jzf-s_vfp` |
| x86 | `x86_64`、`i386_pentium4` |
| MIPS | `mips_24kc`、`mipsel_24kc` |
| LoongArch | `loongarch64_generic` |
| RISC-V | `riscv64_riscv64`（23.05 / 24.10）、`riscv64_generic`（25.12） |

包架构必须匹配路由器的 `DISTRIB_ARCH`。CPU 档位映射见 [runtime-abi](../packaging/openwrt/runtime-abi)

## 打包

本地打包需要能运行 Linux x86_64 容器的 Docker 与 buildx：

```sh
HYPER_PACKAGE_ARCH=aarch64_cortex-a53 sh packaging/openwrt/build.sh 24.10.8 x86_64 1.0.0 dist/feed
```

`build.sh` 编译 hyperd、获取内核，在对应版本的官方 SDK 中生成 IPK（24.10）或 APK（25.12）。软件包内容是预编译程序，任一架构的 SDK 都可为所有架构打包，CI 用 x86_64 SDK。`HYPER_FEED_MODE=test` 同时生成 0.0.0 升级测试包；[release-names.sh](../packaging/openwrt/release-names.sh) 把输出改为发布文件名

## 版本与发布

发布文件名沿用 0.x：`23.05-24.10_luci-app-hyper_<版本>_<架构>.ipk`、`25.12+_luci-app-hyper-<版本>-<架构>.apk` 和 `sha256sums`，路由器的更新程序与 0.x 的都按此查找

`main` 提交按 Conventional Commits 决定下一版本：`fix:`、`perf:`、`refactor:` 递增 Patch，`feat:` 递增 Minor，`BREAKING CHANGE:` / `feat!:` 递增 Major，其余只运行 CI。维护者也可推送 `vX.Y.Z` 标签指定版本。全部测试通过后才创建 GitHub Release；发布前确认构建提交仍是 `main` 最新提交，见 [check-target.sh](../scripts/release/check-target.sh)

## 许可证与来源

- 插件采用 [GPL-3.0-or-later](../LICENSE)
- 随包内核为 mihomo（GPL-3.0-or-later），使用 [core.lock](../scripts/core.lock) 固定的发布构建，不做修改；来源说明随包安装在 `/usr/share/licenses/luci-app-hyper/core/`，同时附带该构建发布的第三方声明
- hyperd 链接的 Go 模块许可证由 `hyper-release licenses` 收集并随包安装

报告问题时不要提交登录凭据、订阅链接或未经脱敏的用户数据
