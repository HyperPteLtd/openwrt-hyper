#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-only
umask 077
exec /usr/bin/ucode -S /etc/hyper/scripts/auto_update.uc "$@"
