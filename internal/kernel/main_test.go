// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Hyper Pte. Ltd.

package kernel

import (
	"os"
	"testing"
)

// The tests download from a repository, which a build names (Repository is empty by default).
// Its releases are tagged by version unless a test names a line (withLine).
func TestMain(m *testing.M) {
	Repository = "example/mihomo"
	origin = "https://github.com/" + Repository + "/releases/download"
	releaseAPI = "https://api.github.com/repos/" + Repository + "/releases"
	os.Exit(m.Run())
}

// withLine: the repository tags its own releases hyper-vX.Y.Z for the test, as a build's
// ReleaseLine names them.
func withLine(t *testing.T) {
	t.Helper()
	ReleaseLine = "hyper"
	t.Cleanup(func() { ReleaseLine = "" })
}
