# SPDX-License-Identifier: GPL-2.0-only
#
# Copyright (C) 2022-2023 ImmortalWrt.org
# Copyright (C) 2026 Hyper Pte. Ltd.

include $(TOPDIR)/rules.mk

LUCI_TITLE:=Hyper for OpenWrt
# One package per CPU (no LUCI_PKGARCH:=all): it bundles the libbox sing-box core
# and hyper-crypt in /usr/libexec/hyper/bin, which scripts/stage-binaries.sh puts
# under root/ before the build. luci-base / rpcd-mod-ucode run the UI and the
# luci.hyper backend; the last three dependencies are sing-box's own. All exist
# from OpenWrt 23.05 on (ucode-mod-digest does not, hence scripts/md5.uc).
LUCI_DEPENDS:= \
	+luci-base \
	+rpcd-mod-ucode \
	+firewall4 \
	+kmod-nft-tproxy \
	+curl \
	+ca-bundle \
	+kmod-inet-diag \
	+kmod-tun

PKG_NAME:=luci-app-hyper
PKG_VERSION:=0.1.0
PKG_LICENSE:=GPL-2.0-only

# htdocs/luci-static/hyper/ is a Vite build (ES modules, already minified).
# LuCI's jsmin / csstidy predate template literals and modern CSS and would
# corrupt it, so leave every .js / .css in this package as it is.
LUCI_MINIFY_JS:=0
LUCI_MINIFY_CSS:=0

define Package/luci-app-hyper/conffiles
/etc/config/hyper
/etc/hyper/session
/etc/hyper/checkin.json
/etc/hyper/ruleset/
/etc/hyper/resources/direct_list.txt
/etc/hyper/resources/proxy_list.txt
endef

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
