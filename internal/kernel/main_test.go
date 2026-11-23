// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package kernel

import (
	"os"
	"testing"
)

// The tests cover online releases too, which this package offers only when a build names the
// repository (Repository is empty by default: the package carries its core).
func TestMain(m *testing.M) {
	Repository = "example/mihomo"
	origin = "https://github.com/" + Repository + "/releases/download"
	releaseAPI = "https://api.github.com/repos/" + Repository + "/releases"
	BundleDir = "/nonexistent/hyper-core"
	os.Exit(m.Run())
}
