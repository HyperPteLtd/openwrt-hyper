// Command runner runs inside the qualification container. All interfaces use isolated QEMU user networks.
//
//	runner [--disk-mib N] [--power-cut] BASE VERSION
package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	diskMiB := flag.Int("disk-mib", 0, "grow the official disk to this size")
	powerCut := flag.Bool("power-cut", false, "cut power once on request and boot again")
	flag.Parse()
	if flag.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: runner [--disk-mib N] [--power-cut] BASE VERSION")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), flag.Arg(1), *diskMiB, *powerCut); err != nil {
		fmt.Fprintln(os.Stderr, "runner:", err)
		os.Exit(1)
	}
}

func run(root, version string, diskMiB int, powerCut bool) error {
	prefix := "openwrt-" + version + "-armsr-armv8-"
	tmp, err := os.MkdirTemp("", "runner-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	disk := filepath.Join(tmp, "router.ext4")
	if err := gunzip(filepath.Join(root, prefix+"generic-ext4-rootfs.img.gz"), disk); err != nil {
		return err
	}
	if diskMiB > 0 {
		info, err := os.Stat(disk)
		if err != nil {
			return err
		}
		size := int64(diskMiB) << 20
		if size < info.Size() {
			return errors.New("refusing to shrink the official disk")
		}
		if err := os.Truncate(disk, size); err != nil {
			return err
		}
		check := exec.Command("e2fsck", "-fy", disk)
		if err := check.Run(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return errors.New("stock image check failed")
			}
		}
		if err := exec.Command("resize2fs", disk).Run(); err != nil {
			return fmt.Errorf("resize2fs: %w", err)
		}
	}
	staged := filepath.Join(root, "overlay")
	var commands []string
	err = filepath.WalkDir(staged, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == staged {
			return err
		}
		rel, _ := filepath.Rel(staged, path)
		name := "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			commands = append(commands, "mkdir "+name)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := "0100644"
		if info.Mode()&0o111 != 0 {
			mode = "0100755"
		}
		commands = append(commands, "rm "+name, "write "+path+" "+name, "set_inode_field "+name+" mode "+mode)
		return nil
	})
	if err != nil {
		return err
	}
	script := filepath.Join(tmp, "stage")
	if err := os.WriteFile(script, []byte(strings.Join(commands, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if err := exec.Command("debugfs", "-w", "-f", script, disk).Run(); err != nil {
		return fmt.Errorf("debugfs: %w", err)
	}
	cmd := []string{"qemu-system-aarch64", "-machine", "virt", "-cpu", "cortex-a57", "-m", "512", "-nographic",
		"-monitor", "none", "-no-reboot", "-kernel", filepath.Join(root, prefix+"generic-kernel.bin"),
		"-drive", "if=none,id=root,format=raw,file=" + disk, "-device", "virtio-blk-pci,drive=root",
		"-append", "console=ttyAMA0 root=/dev/vda rootfstype=ext4 rw rootwait panic=-1",
		"-device", "virtio-net-pci,romfile=,netdev=lan", "-netdev", "user,id=lan,restrict=on",
		"-device", "virtio-net-pci,romfile=,netdev=wan", "-netdev", "user,id=wan"}
	cut, err := boot(cmd, powerCut)
	if err != nil {
		return err
	}
	if powerCut && !cut {
		return errors.New("test guest did not reach the requested power cut")
	}
	if cut {
		// Boot the same disk without running a guest shutdown or a repair tool.
		_, err = boot(cmd, false)
	}
	return err
}

func gunzip(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	r, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func boot(argv []string, permitCut bool) (bool, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return false, err
	}
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		for {
			buf := make([]byte, 65536)
			n, err := stdout.Read(buf)
			if n > 0 {
				chunks <- buf[:n]
			}
			if err != nil {
				return
			}
		}
	}()
	exited := false
	defer func() {
		if !exited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	var raw []byte
	started, activated := false, false
	deadline := time.After(640 * time.Second)
	for {
		select {
		case <-deadline:
			return false, errors.New("firmware timed out")
		case chunk, ok := <-chunks:
			if !ok {
				return false, errors.New("firmware serial closed")
			}
			os.Stdout.Write(chunk)
			raw = append(raw, chunk...)
			if !activated && (bytes.Contains(raw, []byte("Please press Enter")) || bytes.Contains(raw, []byte("Please press Ctrl"))) {
				io.WriteString(stdin, "\n")
				activated = true
			}
			if !started && (bytes.Contains(raw, []byte("root@OpenWrt:")) || bytes.Contains(raw, []byte("root@(none):"))) {
				io.WriteString(stdin, "/bin/sh /root/fw-qualify\n")
				started = true
				raw = raw[:0]
			}
			if permitCut && bytes.Contains(raw, []byte("\nHYPER_POWER_CUT\r\n")) {
				cmd.Process.Kill()
				cmd.Wait()
				exited = true
				fmt.Println("HYPER_HYPERVISOR_CUT")
				return true, nil
			}
			if bytes.Contains(raw, []byte("HYPER_QUALIFICATION_END")) {
				return false, nil
			}
			if len(raw) > 200000 {
				raw = append([]byte(nil), raw[len(raw)-4000:]...)
			}
		}
	}
}
