#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-only
#
# Copyright (C) 2023 ImmortalWrt.org

SCRIPTS_DIR="/etc/hyper/scripts"

for i in "china_ip4" "china_ip6" "gfw_list" "china_list"; do
	"$SCRIPTS_DIR"/update_resources.sh "$i"
done

# Hyper: nodes come from the logged-in PPanel account, not from subscription URLs
"$SCRIPTS_DIR"/sync_subscription.uc
