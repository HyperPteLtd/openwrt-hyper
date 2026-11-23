# SPDX-License-Identifier: GPL-3.0-or-later
#
# Copyright (C) 2026 Hyper Pte. Ltd.

include $(TOPDIR)/rules.mk

LUCI_TITLE:=Hyper for OpenWrt
# One package per CPU: it bundles hyperd and the mihomo core in /usr/libexec/hyper/bin,
# which scripts/stage-binaries.sh puts under root/ before the build. luci.mk makes every
# app without a src/Makefile "all" (LUCI_PKGARCH?=...all), so the arch is set here.
LUCI_PKGARCH:=$(ARCH_PACKAGES)
# rpcd runs `hyperd` as the luci.hyper exec plugin; the network helper needs fw4 with
# TProxy, the TUN driver, full iproute2 (policy rules) and jsonfilter. All exist from
# OpenWrt 23.05 on. dnsmasq (or dnsmasq-full) ships with every image, and so does flock:
# busybox's, which fw4 itself locks with, so no flock package is asked for (some
# firmwares' feeds have none). hyperd makes its own HTTPS requests, diagnostics too.
LUCI_DEPENDS:= \
	+luci-base \
	+rpcd \
	+jsonfilter \
	+firewall4 \
	+kmod-nft-tproxy \
	+kmod-tun \
	+ip-full \
	+ca-bundle

PKG_NAME:=luci-app-hyper
PKG_VERSION:=0.1.0
PKG_LICENSE:=GPL-3.0-or-later

# htdocs/luci-static/hyper/ is a Vite build (ES modules, already minified).
# LuCI's jsmin / csstidy predate template literals and modern CSS and would
# corrupt it, so leave every .js / .css in this package as it is.
LUCI_MINIFY_JS:=0
LUCI_MINIFY_CSS:=0

define Package/luci-app-hyper/conffiles
/etc/config/hyper
/etc/hyper/session
/etc/hyper/checkin.json
/etc/hyper/nodes.json
/etc/hyper/rules/
/etc/hyper/resources/direct_list.txt
/etc/hyper/resources/proxy_list.txt
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
