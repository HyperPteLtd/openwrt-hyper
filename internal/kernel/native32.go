package kernel

import (
	"debug/elf"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

// The tested ARMv7 object attributes specify an application-profile v7 CPU,
// Thumb-2, NEONv1, VFPv3 and hard-float AAPCS. Exact matching rejects objects
// carrying additional processor-specific extensions or a different PCS.
const armv7NEONAttributes = "413b000000616561626900013100000043322e303900060a0741080109020a030c010e0011011204140115001703180119011a021c011e0322012601"

// The linked MIPS flags describe O32/MIPS32r2. Its .MIPS.abiflags declares
// software float, 32-bit registers and no ISA extensions or ASEs.
const mips24kcAttributes = "000020020101000300000000000000000100000000000000"

func native32(file string, e *elf.File, board string) error {
	refused := errors.New("native core requires a verified 32-bit board and ELF ABI")
	if e.Type != elf.ET_EXEC {
		return refused
	}
	for _, p := range e.Progs {
		if p.Type == elf.PT_INTERP || p.Type == elf.PT_DYNAMIC {
			return refused
		}
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var flags [4]byte
	if _, err = f.ReadAt(flags[:], 36); err != nil {
		return err
	}
	flag := e.ByteOrder.Uint32(flags[:])
	var section, expected string
	switch {
	case e.Machine == elf.EM_ARM && strings.HasPrefix(board, "arm_cortex-a") && strings.Contains(board, "_neon") && flag == 0x05000400:
		section, expected = ".ARM.attributes", armv7NEONAttributes
	case e.Machine == elf.EM_MIPS && board == "mipsel_24kc" && flag == 0x70001005:
		section, expected = ".MIPS.abiflags", mips24kcAttributes
	default:
		return refused
	}
	s := e.Section(section)
	if s == nil || s.Size != uint64(len(expected)/2) {
		return refused
	}
	attrs, err := s.Data()
	if err != nil || hex.EncodeToString(attrs) != expected {
		return refused
	}
	return nil
}
