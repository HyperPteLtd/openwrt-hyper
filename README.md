# Hyper for OpenWrt

[![CI](https://github.com/HyperPteLtd/openwrt-hyper/actions/workflows/ci.yml/badge.svg)](https://github.com/HyperPteLtd/openwrt-hyper/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/HyperPteLtd/openwrt-hyper)](https://github.com/HyperPteLtd/openwrt-hyper/releases/latest)

在 LuCI 中登录 Hyper，为接入路由器的设备统一管理代理与 DNS

- 邮箱验证码登录，查看账户与订阅
- 智能分流、全局代理、DNS 保护，按设备选择是否走代理
- 节点选择与并发测速，服务停止时也可测量
- 自定义规则、实时流量、网络诊断与日志
- 配置修改后自动保存，支持检查更新与自动更新

## 安装

需要 LuCI、firewall4，以及与固件匹配的可用软件源。先查看路由器信息：

```sh
cat /etc/openwrt_release
```

按 `DISTRIB_RELEASE` 选择格式，按 `DISTRIB_ARCH` 匹配文件名中的架构。支持的具体架构见[开发指南](docs/development.md#支持的架构)

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

## 使用

1. 在 LuCI 打开 **服务 → Hyper**，使用邮箱验证码登录
2. 在 **概览** 选择节点并启动，也可调整运行模式、分流规则、DNS 和接入设备
3. 在 **账户与订阅** 查看套餐，在 **诊断与日志** 排查连接问题

服务地址自动获取，无需填写。界面默认跟随路由器的 LuCI 语言，支持中文和英文；可在 **设置 → 界面语言** 手动切换

配置修改后自动保存，顶部显示保存状态，失败时可重试。**设置 → 软件更新** 中的“自动检查新版本”和“自动更新”独立控制，自动更新默认关闭

## 开发

使用 Node.js 24，在仓库根目录运行：

```sh
npm --prefix web ci
npm --prefix web run dev
```

打开 `http://localhost:5173` 查看演示数据。前端检查入口：

```sh
npm --prefix web run typecheck
npm --prefix web test
```

构建、后端测试、缓存与发布规则见[开发指南](docs/development.md)

## 反馈与许可证

[提交 Issue](https://github.com/HyperPteLtd/openwrt-hyper/issues) 时请提供固件版本、架构、插件版本和复现步骤，并遮盖账号、令牌等敏感信息

插件采用 [GPL-2.0-only](LICENSE)，随包组件的来源与许可见[开发指南](docs/development.md#许可证与来源)
