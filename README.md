# Hyper for OpenWrt

[![CI](https://github.com/HyperPteLtd/openwrt-hyper/actions/workflows/ci.yml/badge.svg)](https://github.com/HyperPteLtd/openwrt-hyper/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/HyperPteLtd/openwrt-hyper)](https://github.com/HyperPteLtd/openwrt-hyper/releases/latest)

在 LuCI 中登录 Hyper，为接入路由器的设备统一管理代理与 DNS

- 邮箱验证码登录，查看账户、套餐、设备与签到，登录后自动同步节点和分流规则
- 智能分流、全局代理与直连，DNS 保护（直连时只保护 DNS），按设备选择代理或直连
- 节点选择与测速，自定义规则，实时流量、连接记录与日志
- 内核与插件分开更新：首次启动时自动下载，在概览中检查更新，支持全部协议
- 故障时自动回退直连，支持检查更新与自动更新

## 安装

需要 LuCI、firewall4，以及与固件匹配的软件源。先查看路由器信息：

```sh
cat /etc/openwrt_release
```

按 `DISTRIB_RELEASE` 选择格式，按 `DISTRIB_ARCH` 匹配文件名中的架构。支持的架构见[开发指南](docs/development.md#支持的架构)

| OpenWrt | 包管理器 | 下载文件 |
| --- | --- | --- |
| 23.05 / 24.10 | opkg | `23.05-24.10_luci-app-hyper_<版本>_<架构>.ipk` |
| 25.12 | apk | `25.12+_luci-app-hyper-<版本>-<架构>.apk` |

从 [GitHub Releases](https://github.com/HyperPteLtd/openwrt-hyper/releases/latest) 下载对应的一个安装包，上传到 `/tmp`，然后通过 SSH 安装

**OpenWrt 23.05 / 24.10**

```sh
opkg update
opkg install /tmp/23.05-24.10_luci-app-hyper_*.ipk
```

**OpenWrt 25.12**

```sh
apk update
apk add --allow-untrusted /tmp/25.12+_luci-app-hyper-*.apk
```

APK 未由固件信任的仓库签名，因此安装时需要 `--allow-untrusted`

内核完整支持所有协议，解压后约 60–80 MB（视架构而定）。插件首次启动时自动下载最新正式版，优先放在闪存，闪存不够时放在内存（需要 512 MB 内存），重启后重新下载。开机时网络未就绪导致下载失败的，每 10 分钟重试一次。在概览的「更新」卡片中可以检查内核更新、切换存储位置

### 从 0.x 升级

直接安装新版本即可，旧版本内置的更新也会安装它。升级时先停止旧服务并释放网络接管，首次启动时迁移开关、局域网接口、运行模式、设备设置、自定义直连/代理关键词和关闭的分流规则，保留登录状态

## 使用

1. 在 LuCI 打开 **服务 → Hyper**，在 **账户** 用邮箱验证码登录，节点和分流规则会自动同步
2. 在 **概览** 启动代理，调整运行模式、设备、DNS、故障回退和更新设置
3. 在 **代理** 选择节点和测速，在 **规则** 管理我的规则与分流规则，在 **日志** 和 **连接记录** 排查问题

服务地址自动获取，无需填写。设置修改后立即生效。界面语言跟随路由器的 LuCI 语言（**系统 → 系统 → 语言**），支持中文和英文

## 开发与许可证

构建、测试、打包与发布见[开发指南](docs/development.md)

[提交 Issue](https://github.com/HyperPteLtd/openwrt-hyper/issues) 时请提供固件版本、架构、插件版本和复现步骤，并遮盖账号、令牌等敏感信息

插件采用 [GPL-3.0-or-later](LICENSE)，内核的来源见[开发指南](docs/development.md#内核)
