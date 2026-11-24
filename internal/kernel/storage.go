package kernel

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const MiB int64 = 1 << 20

type Volume struct {
	Total, Free int64
	Device      uint64
	InMemory    bool
}
type Capacity struct {
	Persistent, Temporary Volume
	RAM, Available        int64
	Flash                 int64
}

func volume(dir string) (Volume, error) {
	var s syscall.Statfs_t
	if e := syscall.Statfs(dir, &s); e != nil {
		return Volume{}, e
	}
	var st syscall.Stat_t
	if e := syscall.Stat(dir, &st); e != nil {
		return Volume{}, e
	}
	return Volume{int64(s.Bsize) * int64(s.Blocks), int64(s.Bsize) * int64(s.Bavail), uint64(st.Dev), uint64(s.Type) == 0x01021994}, nil
}
func systemCapacity(root, temp string) (Capacity, error) {
	var c Capacity
	var e error
	if c.Persistent, e = volume(root); e != nil {
		return c, e
	}
	if c.Temporary, e = volume(temp); e != nil {
		return c, e
	}
	// Overlay is often a small partition of a larger soldered flash chip.
	c.Flash = c.Persistent.Total
	entries, _ := filepath.Glob("/sys/class/mtd/mtd*/size")
	for _, file := range entries {
		read := func(path string) int64 {
			b, _ := os.ReadFile(path)
			v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 0, 64)
			return v
		}
		size := read(file)
		off := read(filepath.Join(filepath.Dir(file), "offset"))
		if size > 0 && off >= 0 && off <= 1<<48-size && off+size > c.Flash {
			c.Flash = off + size
		}
	}
	b, e := os.ReadFile("/proc/meminfo")
	if e != nil {
		return c, e
	}
	fields := map[string]int64{}
	for _, l := range strings.Split(string(b), "\n") {
		v := strings.Fields(l)
		if len(v) == 3 && v[2] == "kB" {
			n, _ := strconv.ParseInt(v[1], 10, 64)
			fields[v[0]] = n * 1024
		}
	}
	c.RAM = fields["MemTotal:"]
	c.Available = fields["MemAvailable:"]
	// MemTotal excludes kernel reservations. Device tree memory ranges give
	// the installed capacity on exactly-256/512 MiB embedded systems.
	if n := physicalRAM("/sys/firmware/devicetree/base"); n > c.RAM {
		c.RAM = n
	}
	return c, nil
}
func physicalRAM(root string) int64 {
	readCells := func(key string) int {
		b, _ := os.ReadFile(filepath.Join(root, key))
		if len(b) == 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && (b[3] == 1 || b[3] == 2) {
			return int(b[3])
		}
		return 0
	}
	ac, sc := readCells("#address-cells"), readCells("#size-cells")
	if ac == 0 || sc == 0 {
		return 0
	}
	entries, _ := os.ReadDir(root)
	var total int64
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "memory@") && entry.Name() != "memory" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(root, entry.Name(), "reg"))
		stride := (ac + sc) * 4
		if len(b)%stride != 0 {
			return 0
		}
		for len(b) >= stride {
			var n int64
			for _, v := range b[4*ac : stride] {
				n = n<<8 | int64(v)
			}
			if n <= 0 || n > 1<<50 || total > 1<<50-n {
				return 0
			}
			total += n
			b = b[stride:]
		}
	}
	return total
}

// nominalRAM: a router sold with size reaches it. Without a device tree RAM is MemTotal, which
// leaves out what the kernel reserves, so seven eighths of the size count (the package's preinst
// checks the same).
func nominalRAM(ram, size int64) bool { return ram >= size/8*7 }

// capacityError: the router lacks the storage or memory a mode needs; another mode may fit.
type capacityError string

func (e capacityError) Error() string { return string(e) }

// compressed is temporary storage for a network archive. An uploaded archive
// already resides on its own volume and is not counted twice.
func CheckCapacity(c Capacity, mode string, compressed, expanded, peak, replace int64) error {
	if expanded < 1 || expanded > maxAsset || compressed < 0 || compressed > maxAsset || peak <= 0 || peak > 16<<30 || replace < 0 || replace > maxAsset {
		return errors.New("invalid storage estimate")
	}
	var flashNeed, tmpNeed int64
	flashNeed = 16*MiB + 2*MiB // configuration and recovery metadata
	tmpNeed = compressed
	flash := max(c.Persistent.Total, c.Flash)
	switch mode {
	case "double", "single":
		if flash < 128*MiB || !nominalRAM(c.RAM, 256*MiB) {
			return capacityError("persistent mode requires 128 MiB storage and 256 MiB RAM")
		}
		if mode == "double" {
			flashNeed += expanded
		} else {
			tmpNeed += expanded
			if expanded > replace {
				flashNeed += expanded - replace
			}
		}
	case "tmp":
		if flash < 64*MiB || !nominalRAM(c.RAM, 512*MiB) {
			return capacityError("temporary mode requires 64 MiB storage and 512 MiB RAM")
		}
		tmpNeed += expanded
	default:
		return errors.New("storage mode must be double, single or tmp")
	}
	if c.Persistent.Device == c.Temporary.Device {
		if c.Persistent.Free < flashNeed+tmpNeed {
			return capacityError("insufficient shared storage; active core retained")
		}
	} else if c.Persistent.Free < flashNeed || c.Temporary.Free < tmpNeed {
		return capacityError("insufficient free storage; active core retained")
	}
	memory := int64(48)*MiB + peak
	if c.Temporary.InMemory {
		memory += tmpNeed
	}
	if c.Available < memory {
		return capacityError("insufficient trial RAM and 48 MiB system reserve")
	}
	return nil
}
