package kernel

import (
	"os"
	"testing"
)

func TestQualifiedNativeCore(t *testing.T) {
	for _, c := range []struct{ file, target, board, unsupported string }{
		{"HYPER_TEST_ARMV7_NATIVE", "armv7", "arm_cortex-a9_neon", "arm_cortex-a9_vfpv3-d16"},
		{"HYPER_TEST_MIPSLE_NATIVE", "mipsle-softfloat", "mipsel_24kc", "mipsel_4kec"},
	} {
		t.Run(c.target, func(t *testing.T) {
			file := os.Getenv(c.file)
			if file == "" {
				t.Skip("requires local protocol qualification build")
			}
			if err := checkELF(file, c.target, c.board); err != nil {
				t.Fatal(err)
			}
			if err := checkELF(file, c.target, c.unsupported); err == nil {
				t.Fatal("accepted native core for an unqualified CPU")
			}
		})
	}
}
