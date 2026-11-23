# 开发指南

[返回 README](../README.md)

## 代码结构

| 目录 | 内容 |
| --- | --- |
| `web/` | Preact + TypeScript 界面，构建为 LuCI 内嵌的静态页面 |
| `cmd/hyperd/` | 路由器管理进程：luci.hyper 接口、核心与网络接管、后台任务 |
| `internal/` | hyperd 的模块：mihomo 配置生成、节点与订阅同步、账号、解密、更新、UCI |
| `luci-app-hyper/` | 软件包定义、init 脚本、网络接管脚本和首次安装脚本 |
| `scripts/` | 核心暂存、SDK 打包、规则校验和发布工具 |
| `tests/` | 构建脚本回归、网络脚本、核心渲染和路由器集成测试；`tests/golden/` 为旧实现的参考数据 |

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
node --test tests/build_scripts.test.mjs tests/stage_binaries.test.mjs tests/proxy_conflicts.test.mjs
node scripts/bundled-rules.mjs
go test -race ./...
sh tests/network_test.sh
MIHOMO=/tmp/hyper-core/mihomo sh tests/render_test.sh
```

hyperd 只面向 Linux（使用 Linux 专有的进程凭据、挂载命名空间和 netlink），Go 测试和网络测试需在 Linux 主机、虚拟机或容器中运行，macOS 上无法编译

构建脚本测试使用 Node 原生 `node:test`，以临时目录、真实 Shell/Git/tar 和网络、编译器替身覆盖命名、版本保护、源码校验、架构映射及 SDK 打包边界

hyperd 的 Go 测试用测试二进制充当假核心，覆盖启动、切换节点、测速、流量、设备列表刷新、故障恢复和停止的完整流程，只读接口与 `tests/golden/rpc/` 对照。`network_test.sh` 在非特权的 user + network 命名空间里用真实 nftables 和 iproute2 执行网络接管脚本（无权限时需 root）。`render_test.sh` 对每种配置运行 `mihomo -t`，并在命名空间里实际启动核心，确认控制接口、随包规则集和监听端口

[router-test.sh](../tests/router-test.sh) 在一次性 OpenWrt 容器中验证依赖安装、rpcd、启停和更新 worker。导入与目标架构匹配的官方 rootfs 后，设置 `IMAGE`、`PLATFORM` 并运行：

```sh
sh tests/router-test.sh "$IMAGE" /tmp/hyper-core "$PLATFORM"
```

第二个参数也可传入已构建的 IPK / APK。镜像导入与运行方式见 [CI 工作流](../.github/workflows/ci.yml)，集成测试应使用对应架构的原生执行环境

## 界面与运行状态

国际化统一使用 [i18n.ts](../web/src/lib/i18n.ts)：中文源文案作为消息键，英文位于 [en.ts](../web/src/lib/locales/en.ts)，通过 `t()` 翻译和插值。TypeScript 检查消息键，测试检查中英文参数一致性。语言默认跟随 LuCI，手动选择优先；套餐名称、地区名称与接口参数使用同一 locale

[cache.ts](../web/src/lib/cache.ts) 合并并发读取，并按路由器和账户隔离展示数据。账户资料、套餐信息与累计用量缓存 1 分钟，节点和配置缓存 30 秒；可持久保存的展示数据最多保留 7 天，重开页面先展示旧值再后台校准。退出登录或换号会清理旧缓存，不持久保存登录凭据、订阅链接或节点连接配置。在本页面登录成功后（不是打开时已登录），[account-warm.ts](../web/src/lib/account-warm.ts) 在新账户的状态到达后逐个读取各页面首次打开会请求的账户资料、套餐、设备、邀请统计、套餐列表、支付方式、站点配置、公告和签到状态，参数与页面完全一致，之后打开账户、套餐页直接用缓存；退出或换号时停止。“规则”页放我的规则与分流规则

节点延迟连同测量时间按账户和节点身份保存。刷新失败保留上次结果，自动选择仅使用本次测量结果。测速使用独立的临时进程，服务停止时也可运行，最多同时测量 6 个节点并逐项返回；手选节点或启停服务会取消旧测速。运行状态、路由器实时流量、诊断和支付结果不复用读取缓存；服务控制等待首次真实状态返回，手动检查更新也会重新请求

配置写入使用共享队列，串行提交并合并快速修改；文本输入先校验和防抖。切页不会丢失待提交内容，失败保留修改并在导航右侧提供重试；退出或换号时取消未发送的旧操作

启停 RPC 提交操作后立即返回，后台 worker 串行执行并记录进度。连续操作保留最新意图，停止可中断尚未完成的启动。启动在本地控制 API 和核心的监听端口就绪、接管完成，并且随后连续 4 次（每秒一次）检查控制 API 和接管都正常后才算完成，刚起来就倒下的核心或接管会让这次启动失败，而不是先显示已连接。核心退出时立即失败，并带上核心最后一条错误；其他检查失败一两次只让连续计数重来，第三次才失败，所以仍在稳定的接管（或繁忙路由器偶尔慢一拍的应答）不会误判。确认完成前看门狗不介入。启动失败后 30 秒自动重试，之后间隔翻倍，最长 5 分钟；任何新操作或停用服务都会取消重试。失败原因以错误码返回，界面显示对应的中文（[errors.ts](../web/src/lib/errors.ts)），详细原因写在日志里；`web/tests/errors.test.ts` 读取 Go 源码，hyperd 新增的错误码没有中文时测试失败。界面先反馈操作中状态，再依据后台结果更新；运行时长按秒显示并与真实进程时长校准

账户请求、诊断及更新检查通过后台任务执行，避免占用 rpcd 阻塞控制和状态读取。节点同步由登录、定时任务和手动刷新共用一把锁，按会话合并请求；换号或退出后丢弃旧结果，提交前重新读取配置，保留用户最新的节点选择和启停状态

## 路由器运行结构

procd 运行两个实例：`hyperd serve` 和 `hyperd guard`。rpcd 通过 exec 插件 `/usr/libexec/rpcd/luci.hyper` 调用 `hyperd call`，请求经 root 专用的 unix socket 转给 serve，会话令牌不会传给 serve

- serve 生成 mihomo 配置（[internal/mihomo](../internal/mihomo/render.go)），写入 `/var/run/hyper/core`，先用 `mihomo -t` 校验，再启动核心；控制接口可能先于监听端口应答，所以等 DNS、mixed 和 TProxy 端口都在监听后才调用 [network](../luci-app-hyper/root/usr/libexec/hyper/network) 接管网络。核心退出后自动重启
- 核心按 OpenClash 的方式运行：serve 直接以 root 的用户 ID 启动它，拥有全部能力，不加沙箱、不设能力上限和 `no_new_privs`，TUN 模式直接打开路由器的 `/dev/net/tun`；只是它的组换成 uci-defaults 创建的 `hyper` 组（真实、有效、保存和文件系统组），没有附加组，防火墙按 `meta skgid` 放行该组的连接。不用系统自带的 nogroup，因为以 nogroup 运行的其他进程也会绕过接管。核心随 serve 退出（父进程死亡信号），在自己的进程组里，停止时整组结束。校验和测速用的临时核心也一样。缺少 `hyper` 组或它是 root 的组时不启动核心。核心的文件都归 root 私有；完整启动前，如果 `/var/run/hyper/core` 里有不属于 root 的文件（之前的版本以独立账户运行时留下的），整个目录会重建。核心可打开 1000000 个文件（与 OpenClash 一致）：init 脚本让 procd 给 serve 设这个上限，serve 启动核心前自己再设一次，免得 Go 把最初的软上限交给核心；内核的 `fs.nr_open` 更低时取 `fs.nr_open`，仍设不上时日志警告。核心日志保留 “too many open files”。核心崩溃（panic、fatal error 或信号）时，第一行写成带时间的错误日志，后面只保留出错 goroutine 的调用栈，其余几百个 goroutine 的转储不写入日志（只记一次）；运行时自己的 `runtime:` 行（内存耗尽等）也记为错误。MIPS 上 hyperd 和核心的 TLS 都不使用后量子密钥交换 X25519MLKEM768：Go 在 MIPS 上做这部分运算时会非对齐读取，QEMU 里直接 SIGBUS，真机上每次读取都陷入内核。`HYPER_E2E_ROOT=1 sh tests/e2e_test.sh` 以真实 root 运行端到端测试并检查核心的身份、能力和文件数上限（CI 默认如此）
- network 脚本在 fw4 上创建带归属标记的 `inet hyper` 表，加入策略路由、fw4 放行规则和 dnsmasq 上游。TProxy 与 TUN 没有设置项，每次启动时自动选择：防火墙接受 network 脚本的 tproxy 规则（`nft -c` 只检查、不改动）就用 TProxy，否则有 `/dev/net/tun` 时用 TUN，都没有时仍用 TProxy，由接管步骤报出缺少什么；所选方式写入日志，运行期间的接口变化不会切换。升级时 uci-defaults 删除旧版本留下的 `config.proxy_mode`。测试用 `HYPERD_METHOD=tun` 强制 TUN（`E2E_METHOD=tun sh tests/e2e_test.sh`）；释放时只删除带标记的对象
- 路由器直连的网段（LAN、WAN 和下发的 IPv6 前缀）与入站连接的回包不经过核心；接口变化时 hotplug 先执行 `network refresh` 更新这组集合，再执行 `hyperd notify-network`：重新生成的配置与运行中的相同就不重启，避免断开现有连接；IPv6 上行出现或消失会改变配置，因此会重启。fake-ip 在接管流量时固定开启，IPv6 按上行自动接管，二者都不是设置项。所有端口都交给核心，NTP 始终直连；接管期间 dnsmasq 缓存清零，释放时恢复。TProxy 监听端口只接受被拦截的流量，局域网或 WAN 直接连接会被拒绝；fake-ip 地址可以 ping 通
- guard 检查 serve 的心跳和核心控制接口，连续失败时执行 `network fault` 释放接管，局域网恢复直连。核心恢复后 serve 重新接管
- `/etc/init.d/hyper stop` 先放置 `/var/run/hyper/stopping` 标记并释放接管，procd 随后才通知 serve 退出（最多等 20 秒）。标记存在期间 serve 不恢复、不修复、不重启接管，network 脚本也拒绝 `up` 和 `repair`；下次启动时清除
- 节点以 mihomo 格式保存在 `/etc/hyper/nodes.json`，组名沿用 `main-out` / `cfg-<id>-out`。切换节点通过控制接口即时生效，不重启
- 每日订阅同步和更新检查由 serve 自行调度，不使用 cron

启动前会检查 OpenClash、PassWall、PassWall2、ShadowsocksR Plus+、HomeProxy、Nikki、Momo、v2rayA 和 Mihomo 是否正在接管流量，仅通过各插件的服务脚本停止活动服务，保留配置。确认要接管后，再用各自的服务脚本取消它们的开机自启，否则重启后它们会把流量抢回去；取消不了时不接管，启动被取消时也不改开机设置。要换回原插件，重新启用它即可。退出失败或规则未释放时停止接管，避免两套透明代理同时生效

## 服务发现与规则

服务发现由 [endpoints.go](../internal/account/endpoints.go) 管理，使用内置地址与加密 DNS TXT 记录更新备用列表。地址仅在路由器内存中缓存，有效期 30 分钟，查询失败保留旧值并在 5 分钟后重试。登录、验证码和不带幂等键的写请求不会跨地址重复发送。

订阅里的 `anytls://` 链接按 Hyper App 的规则转换为 mihomo 节点（[internal/nodes](../internal/nodes/nodes.go)），其他协议跳过。配置结构：境外域名经代理使用加密 DNS，国内域名和节点域名使用国内 DNS；接管流量时使用 fake-ip（仅 DNS 模式不用）；设备规则按 MAC 对应的地址列表匹配，serve 每 30 秒依据邻居表刷新，无需重启。直连名单中的设备同时在防火墙层放行，核心故障时不受影响

节点与规则更新自动选择可用连接：先直连，网络或服务端故障（包括传输中途断开）时尝试本机代理（只发 https，并确认连接已由核心，即 root 接受），鉴权失败不重复请求。超过 16 MB 的规则列表直接拒绝，不会截断后保存。规则先写临时文件再原子替换，新索引发布后才删除旧文件，失败保留原文件

闪存上的文件（节点、会话、规则、名单、签到、`/etc/config/hyper`）统一经 [internal/atomicfile](../internal/atomicfile/atomicfile.go) 替换：先写同目录下的 `.<文件名>-<随机数字>` 临时文件并同步到存储，再改名替换，最后同步目录，断电后只会看到旧内容或新内容。写到一半被杀留下的临时文件，serve 下次启动时清理（`/etc/config` 只清 hyper 自己的）。会话内容没变时不重写，每天同步订阅不再多写一次闪存；会话的检查与写入是一步，同步途中退出登录或换号不会被旧会话覆盖

随包规则集为 MetaCubeX meta-rules-dat 的 `geoip-cn.mrs`、`geosite-cn.mrs`，来源固定在 [SOURCES](../luci-app-hyper/root/etc/hyper/rule-set/SOURCES)，SHA-256 在 [SHA256SUMS](../luci-app-hyper/root/etc/hyper/rule-set/SHA256SUMS)。校验与按来源恢复：

```sh
node scripts/bundled-rules.mjs
node scripts/bundled-rules.mjs --fetch
```

恢复前会核对 zstd 文件头与校验和，不修改固定的来源

服务开启期间，serve 每周从同一来源下载这两份名单（[internal/cnlist](../internal/cnlist/cnlist.go)），直连失败再经核心代理；下载内容须解压校验通过且与在用的不同才写入 `/etc/hyper/cnlists`，然后让运行中的核心重新加载，不重启。随包副本用于首次启动和下载失败；服务关闭时不下载

## 核心构建

[core.lock](../scripts/core.lock) 固定 mihomo 核心的源码仓库、提交、归档 SHA-256、构建标签和 Go 工具链。[mihomo-trim.mjs](../scripts/mihomo-trim.mjs) 在编译前裁掉路由器用不到的协议和入站，未被引用的实现由链接器丢弃

hyperd 从本仓库 `cmd/hyperd` 编译，版本号取 Makefile 的 `PKG_VERSION`。两者均静态编译，使用 `CGO_ENABLED=0`、`-mod=readonly`、`-trimpath`、`-buildvcs=false` 和 `-s -w -buildid=`

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

CI 的前端检查、测试与构建只执行一次。核心与 hyperd 按 7 种 CPU 配置共享产物，amd64 直接复用通过启动测试的二进制。精确缓存键包含 CPU 配置、源码锁定文件、构建脚本与 hyperd 源码；命中后跳过重复编译，amd64 仍运行启动测试。二进制用 tar 保留执行权限，所有 SDK 任务复用这些产物

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

CI 的外部下载都会重试，且每次尝试都有时限（SDK 镜像 3 分钟、rootfs 2 分钟、SDK 软件源和核心源码 5 分钟），任务超时留出全部重试的时间，卡住的下载会被重试，而不是拖到任务超时。Actions 固定到提交 SHA，Dependabot 每月合并成一个更新 PR。

自动发布在下载产物前及正式发布前确认构建提交仍是 `main` 最新提交。期间如有新提交，旧运行保留测试与构建结果并跳过发布。发布目标固定为实际构建的提交；已有标签指向其他提交、手动触发的标签被删除或远端检查失败时直接报错，不移动标签，也不把旧包关联到新代码。逻辑见 [check-target.sh](../scripts/release/check-target.sh)

## 许可证与来源

- 插件采用 [GPL-3.0-or-later](../LICENSE)
- 随包 mihomo 核心按 [core.lock](../scripts/core.lock) 固定的源码编译并裁剪，采用 GPL-3.0
- 早期版本源自 [HomeProxy](https://github.com/immortalwrt/homeproxy)（GPL-2.0-only）。改用 hyperd 后不再包含 HomeProxy 的代码

报告问题时不要提交登录凭据、订阅链接或未经脱敏的用户数据
