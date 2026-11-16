# 开发指南

[返回 README](../README.md)

## 代码结构

| 目录 | 内容 |
| --- | --- |
| `web/` | Preact + TypeScript 界面，构建为 LuCI 内嵌的静态页面 |
| `luci-app-hyper/` | 软件包定义、rpcd 接口和路由器服务 |
| `hyper-crypt/` | Go 编写的解密工具 |
| `scripts/` | 核心暂存、SDK 打包、规则校验和发布工具 |
| `tests/` | 构建脚本回归、ucode 测试和路由器集成测试 |

以下命令均从仓库根目录执行。前端和 Node 脚本使用 Node.js 24；Go 工具链版本以 [core.lock](../scripts/core.lock) 为准

## 开发与测试

```sh
npm --prefix web ci
npm --prefix web run dev
```

开发服务器位于 `http://localhost:5173`，在 LuCI 之外使用演示数据。构建命令 `npm --prefix web run build` 会写入 `luci-app-hyper/htdocs/luci-static/hyper/`

```sh
npm --prefix web run typecheck
npm --prefix web test
node --test tests/build_scripts.test.mjs tests/stage_binaries.test.mjs
(cd hyper-crypt && go test ./...)
node scripts/bundled-rules.mjs
```

构建脚本测试使用 Node 原生 `node:test`，以临时目录、真实 Shell/Git/tar 和网络、编译器替身覆盖命名、版本保护、源码校验、架构映射及 SDK 打包边界

Linux 主机安装 ucode 及所需扩展、nftables、curl 后，可运行 `sh tests/run.sh`。自编译的 ucode 可通过 `UCODE`、`UCODE_LIB` 指定路径。将已暂存核心加入 `PATH` 后，`tests/render_test.sh` 会校验配置并实际启动核心

[router-test.sh](../tests/router-test.sh) 在一次性 OpenWrt 容器中验证依赖安装、rpcd、启停和更新 worker。导入与目标架构匹配的官方 rootfs 后，设置 `IMAGE`、`PLATFORM` 并运行：

```sh
sh tests/router-test.sh "$IMAGE" /tmp/hyper-core "$PLATFORM"
```

第二个参数也可传入已构建的 IPK / APK。镜像导入与运行方式见 [CI 工作流](../.github/workflows/ci.yml)，集成测试应使用对应架构的原生执行环境

## 界面与运行状态

国际化统一使用 [i18n.ts](../web/src/lib/i18n.ts)：中文源文案作为消息键，英文位于 [en.ts](../web/src/lib/locales/en.ts)，通过 `t()` 翻译和插值。TypeScript 检查消息键，测试检查中英文参数一致性。语言默认跟随 LuCI，手动选择优先；套餐名称、地区名称与接口参数使用同一 locale

[cache.ts](../web/src/lib/cache.ts) 合并并发读取，并按路由器和账户隔离展示数据。账户资料、套餐信息与累计用量缓存 1 分钟，节点和配置缓存 30 秒；可持久保存的展示数据最多保留 7 天，重开页面先展示旧值再后台校准。退出登录或换号会清理旧缓存，不持久保存登录凭据、订阅链接或节点连接配置

节点延迟连同测量时间按账户和节点身份保存。刷新失败保留上次结果，自动选择仅使用本次测量结果。测速使用独立的临时进程，服务停止时也可运行，最多同时测量 6 个节点并逐项返回；手选节点或启停服务会取消旧测速。运行状态、路由器实时流量、诊断和支付结果不复用读取缓存；服务控制等待首次真实状态返回，手动检查更新也会重新请求

配置写入使用共享队列，串行提交并合并快速修改；文本输入先校验和防抖。切页不会丢失待提交内容，失败保留修改并在导航右侧提供重试；退出或换号时取消未发送的旧操作

启停 RPC 提交操作后立即返回，后台 worker 串行执行并记录进度。连续操作保留最新意图，停止可中断尚未完成的启动。启动以本地控制 API 就绪为准，界面先反馈操作中状态，再依据后台结果更新；运行时长按秒显示并与真实进程时长校准

账户请求、诊断及更新检查通过后台任务执行，避免占用 rpcd 阻塞控制和状态读取。节点同步由登录、定时任务和手动刷新共用一把锁，按会话合并请求；换号或退出后丢弃旧结果，提交前重新读取配置，保留用户最新的节点选择和启停状态

启动前会检查 OpenClash、PassWall、PassWall2、ShadowsocksR Plus+、HomeProxy、Nikki、Momo、v2rayA 和 Mihomo 是否正在接管流量，仅通过各插件的服务脚本停止活动服务，保留配置和开机设置。退出失败或规则未释放时停止接管，避免两套透明代理同时生效

## 服务发现与规则

服务发现由 [api_endpoints.uc](../luci-app-hyper/root/etc/hyper/scripts/api_endpoints.uc) 管理，使用内置地址与加密 DNS TXT 记录更新备用列表。地址仅在路由器内存中缓存，有效期 30 分钟，查询失败保留旧值并在 5 分钟后重试。登录、验证码和不带幂等键的写请求不会跨地址重复发送。升级迁移会删除旧手动地址配置，并清理来自非内置地址的登录凭据

配置生成参考 Hyper 移动客户端的核心适配：显式 HTTP 下载客户端、DNS 回答筛选、默认 TUN 协议栈与 DNS 缓存，并通过 `initial_path` 使用随包规则离线启动。路由器使用自身的 dnsmasq / nftables 配置和原有规则数据源

节点与规则更新自动选择可用连接：先直连，网络或服务端故障时尝试本机代理，鉴权失败不重复请求。规则在安装、升级、启动和同步时统一为 `root:sing-box` 所有，目录 `750`、文件 `640`；同步先写临时文件，确认权限后原子替换，失败保留原文件

规则的来源提交、长度和 SHA-256 固定在 [manifest.json](../luci-app-hyper/root/etc/hyper/rule-set/manifest.json)。校验与按清单恢复：

```sh
node scripts/bundled-rules.mjs
node scripts/bundled-rules.mjs --fetch
```

恢复前会核对 SRS 文件头、长度与校验和，不修改清单中的版本

## 核心构建

[core.lock](../scripts/core.lock) 固定 libbox 源码提交、归档 SHA-256、SDK 版本和 Go 工具链。当前使用 libbox 0.1.3、Go 1.25.5；核心版本从固定源码的 `internal/singbox/runtime.go` 读取，Revision 写入完整提交号

路由器构建标签为：

```text
with_quic,with_dhcp,with_wireguard,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0
```

仅路由器包使用此配置，不改变 libbox 通用 CLI 默认值。核心与解密工具均静态编译，使用 `CGO_ENABLED=0`、`-mod=readonly`、`-trimpath`、`-buildvcs=false` 和 `-s -w -buildid=`；核心另加 `-checklinkname=0`

单独暂存二进制需要 curl、tar、awk，以及 Go 或 Docker：

```sh
OUT_DIR=/tmp/hyper-core CACHE_DIR=/tmp/hyper-build-cache \
  sh scripts/stage-binaries.sh aarch64_cortex-a53
```

脚本先校验源码归档 SHA-256，再拒绝越界路径和链接，检查 SDK 版本后编译，不执行下载源码中的构建脚本，也不回退到通用预编译核心。本机 Go 固定使用 `GOTOOLCHAIN=go1.25.5`，缺少该工具链时可自动下载；没有 Go 时使用对应 golang Docker 镜像

`OUT_DIR` 默认是包内 `usr/libexec/hyper/bin`；`CACHE_DIR` 默认为 `dist/.cache`，只存源码归档。本机保留 Go 标准缓存及显式 `GOMODCACHE` / `GOCACHE`。Docker 通过 tar 流传入源码、传出产物，以 `hyper-router-go-cache` volume 复用 Go 缓存，不要求挂载工作区

## 支持的架构

当前 CI 为以下 11 个包架构构建两种固件格式，共 22 个 SDK 目标：

| 架构类别 | 包架构 |
| --- | --- |
| ARM64 | `aarch64_cortex-a53`、`aarch64_cortex-a72`、`aarch64_generic` |
| ARMv7 | `arm_cortex-a7_neon-vfpv4`、`arm_cortex-a9_vfpv3-d16`、`arm_cortex-a15_neon-vfpv4` |
| x86 | `x86_64`、`i386_pentium4` |
| MIPS | `mipsel_24kc` |
| LoongArch | `loongarch64_generic` |
| RISC-V | `riscv64_riscv64`（23.05 / 24.10）、`riscv64_generic`（25.12） |

包架构必须匹配路由器的 `DISTRIB_ARCH`，不要仅凭处理器系列选包。`stage-binaries.sh --target-id <包架构>` 使用与编译相同的映射，查询时不会下载或编译

## 本地打包与 CI

先安装前端依赖。本地完整打包还需要能运行 Linux x86_64 容器的 Docker：

```sh
npm --prefix web ci
sh scripts/build-packages.sh mediatek/filogic
```

不传目标时使用 `x86/64`。`RELEASES` 可指定 SDK 版本，默认值见 [build-packages.sh](../scripts/build-packages.sh)，输出为 `dist/` 中的 IPK / APK 和 `sha256sums`

CI 的前端检查、测试与构建只执行一次。核心与解密工具按 7 种 CPU 配置共享产物，amd64 直接复用通过启动测试的二进制。精确缓存键包含 CPU 配置、源码锁定文件、构建脚本与解密工具源码；命中后跳过重复编译，amd64 仍运行启动测试。二进制用 tar 保留执行权限，所有 SDK 任务复用这些产物

SDK 阶段运行对应版本的官方镜像，加载 base、packages、luci 与本地应用 feed，并保留固定版本及完整运行依赖声明。[sdk-package.sh](../scripts/sdk-package.sh) 使用 SDK 自身的工具与打包规则组装已编译应用，不重新编译路由器运行依赖；文件所有者校验和官方签名密钥初始化仍执行。当前 SDK 的单包构建不签名 APK，仓库索引签名属于独立步骤。本地打包使用同一打包入口。新增源码目录、构建依赖或 Makefile 钩子时，脚本会停止并要求重新评估该流程

## 版本与发布

显示版本和下载文件名不带 `-rN` 修订后缀；内部包元数据仍保留 SDK 格式，维护包管理器的升级顺序。[package-name.sh](../scripts/package-name.sh) 统一 CI 与本地文件命名，重命名后生成校验和。更新接口可识别旧式和新式附件名

`main` 提交按 Conventional Commits 决定下一版本。首次发布使用 Makefile 版本，之后从最近的正式标签递增：

| 提交类型 | 版本变化 |
| --- | --- |
| `fix:`、`perf:`、`refactor:` | Patch |
| `feat:` | Minor |
| `BREAKING CHANGE:` / `feat!:` | Major |
| `docs:`、`test:`、`ci:`、`chore:` | 仅运行 CI |

发布前保留全部测试：22 个 SDK 目标打包，OpenWrt 23.05、24.10、25.12 分别在原生 x86_64 与 ARM64 环境执行安装测试。通过后才创建 GitHub Release，附上软件包和 `sha256sums`；维护者也可推送 `vX.Y.Z` 标签指定版本。标签必须为正式 SemVer 版本，预发布标签或其他格式会在 SDK 打包前被拒绝

自动发布在下载产物前及正式发布前确认构建提交仍是 `main` 最新提交。期间如有新提交，旧运行保留测试与构建结果并跳过发布。发布目标固定为实际构建的提交；已有标签指向其他提交、手动触发的标签被删除或远端检查失败时直接报错，不移动标签，也不把旧包关联到新代码。逻辑见 [check-target.sh](../scripts/release/check-target.sh)

## 许可证与来源

- LuCI 插件源自 [HomeProxy](https://github.com/immortalwrt/homeproxy)，采用 [GPL-2.0-only](../LICENSE)
- 随包 sing-box 核心来自 [HyperPteLtd/libbox](https://github.com/HyperPteLtd/libbox)，采用 GPL-3.0-or-later

报告问题时不要提交登录凭据、订阅链接或未经脱敏的用户数据
