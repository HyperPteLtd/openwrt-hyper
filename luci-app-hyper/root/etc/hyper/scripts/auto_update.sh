#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-only
umask 077
HYPER_RPC_TASK=1 exec /usr/bin/ucode -S /etc/hyper/scripts/auto_update.uc "$@"
