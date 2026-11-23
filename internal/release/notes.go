package release

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	sumLine     = regexp.MustCompile(`^([a-f0-9]{64})  ([A-Za-z0-9._-]+)$`)
)

// Preview describes a develop build published as a pre-release.
type Preview struct {
	Tag    string // v<VERSION>-preview.<N>
	Commit string // the develop commit it was built from
}

// RenderNotes renders the bilingual installation notes of a GitHub Release from the staged
// assets directory and the SHA256SUMS beside it. A non-nil preview links its own tag and
// adds the preview caveats.
func RenderNotes(repo, version, assets string, preview *Preview) (string, error) {
	if !repoPattern.MatchString(repo) {
		return "", errors.New("invalid GitHub repository")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Clean(assets)), "SHA256SUMS"))
	if err != nil {
		return "", err
	}
	sums := strings.TrimRight(string(raw), "\n")
	// Preview n carries revision n (1.0.1-r3); the release carries the revision of its assets.
	pkg := version
	if preview != nil {
		n, err := strconv.Atoi(strings.TrimPrefix(preview.Tag, "v"+version+"-preview."))
		if err != nil || n < 1 || preview.Tag != fmt.Sprintf("v%s-preview.%d", version, n) {
			return "", errors.New("invalid preview tag or commit")
		}
		pkg = RevisionVersion(version, n)
	} else if n, err := AssetRevision(version, assets); err == nil {
		pkg = RevisionVersion(version, n)
	} else {
		return "", err
	}
	names := map[string]bool{}
	for _, n := range AllNames(pkg) {
		names[n] = true
	}
	entries, err := os.ReadDir(assets)
	if err != nil {
		return "", err
	}
	if len(entries) != len(names) {
		return "", errors.New("expected exactly the per-architecture Hyper installer packages")
	}
	for _, e := range entries {
		if !names[e.Name()] {
			return "", errors.New("expected exactly the per-architecture Hyper installer packages")
		}
	}
	listed := map[string]bool{}
	lines := strings.Split(sums, "\n")
	for _, line := range lines {
		m := sumLine.FindStringSubmatch(line)
		if m == nil || !names[m[2]] {
			return "", errors.New("release checksum entries do not match the installers")
		}
		listed[m[2]] = true
	}
	if len(lines) != len(names) || len(listed) != len(names) {
		return "", errors.New("release checksum entries do not match the installers")
	}
	tag, intro, introZh := "v"+version, "", ""
	if preview != nil {
		if !strings.HasPrefix(preview.Tag, tag+"-preview.") || !commitSHA.MatchString(preview.Commit) {
			return "", errors.New("invalid preview tag or commit")
		}
		tag = preview.Tag
		short := preview.Commit[:7]
		intro = "Preview of " + strings.TrimPrefix(tag, "v") + ", built from " + short + " on develop.\n\n" +
			"- The packages carry version " + pkg + "; each later preview and the stable release take a higher revision, so they upgrade it directly.\n" +
			"- The router's **Check for updates** never offers previews.\n\n"
		introZh = strings.TrimPrefix(tag, "v") + " 预览版，由 develop 上的 " + short + " 构建。\n\n" +
			"- 安装包版本号是 " + pkg + "，之后的预览版和正式版修订号递增，可直接覆盖升级。\n" +
			"- 路由器的「检查更新」不会提供预览版。\n\n"
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, tag)
	var rows []string
	link := func(format, arch string) string {
		if !slices.Contains(Arches[format], arch) {
			return "—"
		}
		name := PackageName(pkg, format, arch)
		return fmt.Sprintf("[%s](%s/%s)", name, url, name)
	}
	for _, arch := range AllArches {
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", arch, link("ipk", arch), link("apk", arch)))
	}
	r := strings.NewReplacer(
		"{table}", strings.Join(rows, "\n"),
		"{ipk}", PackageName(pkg, "ipk", "<arch>"),
		"{apk}", PackageName(pkg, "apk", "<arch>"),
		"{ipk_zh}", PackageName(pkg, "ipk", "<架构>"),
		"{apk_zh}", PackageName(pkg, "apk", "<架构>"),
		"{repo}", repo,
		"{tag}", tag,
		"{intro}", intro,
		"{intro_zh}", introZh,
	)
	return r.Replace(notesTemplate), nil
}

const notesTemplate = "{intro}## Downloads\n\n" +
	"Pick the package for the router's architecture: run `. /etc/openwrt_release; echo $DISTRIB_ARCH` on the router and find it below. OpenWrt 23.05 / 24.10 use the IPK (opkg), 25.12 uses the APK (apk). Each package declares its architecture, so opkg / apk refuse a package built for another one.\n\n" +
	"| Architecture | 23.05 / 24.10 · IPK | 25.12 · APK |\n| --- | --- | --- |\n{table}\n\n" +
	"Checksums are in the `SHA256SUMS` asset. Download it next to your package and run `grep \" <package>$\" SHA256SUMS | sha256sum -c -` (this also works with BusyBox on the router).\n\n" +
	"### OpenWrt 23.05 / 24.10 · IPK\n\n```sh\nopkg install ./{ipk}\n```\n\n" +
	"### OpenWrt 25.12 · APK\n\n```sh\napk add --no-network --allow-untrusted ./{apk}\n```\n\n" +
	"For an offline installation, make sure the firmware's system dependencies are available. If upgrading an older Cortex-A53 APK, follow the [offline upgrade instructions](https://github.com/{repo}/blob/{tag}/packaging/openwrt/README.md#manual-upgrade-from-an-older-cortex-a53-apk) to keep the existing network components.\n\n" +
	"After installing, open **Services → Hyper** in LuCI. The mihomo core is selected separately; offline, you can import its original archive for the router's architecture.\n\n" +
	"---\n\n" +
	"{intro_zh}## 安装包\n\n" +
	"在路由器上运行 `. /etc/openwrt_release; echo $DISTRIB_ARCH` 查看架构，然后在上方 Downloads 表格中找到对应的一行。OpenWrt 23.05 / 24.10 用 IPK（opkg），25.12 用 APK（apk）。安装包写明了所属架构，装错架构时 opkg / apk 会拒绝安装。\n\n" +
	"校验值在附件 `SHA256SUMS` 中。把它下载到安装包所在目录，运行 `grep \" <安装包文件名>$\" SHA256SUMS | sha256sum -c -`（路由器上的 BusyBox 也支持）。\n\n" +
	"### OpenWrt 23.05 / 24.10 · IPK\n\n```sh\nopkg install ./{ipk_zh}\n```\n\n" +
	"### OpenWrt 25.12 · APK\n\n```sh\napk add --no-network --allow-untrusted ./{apk_zh}\n```\n\n" +
	"离线安装前，请准备好固件需要的系统依赖。已安装旧版 Cortex-A53 APK 的用户，可按[离线升级步骤](https://github.com/{repo}/blob/{tag}/packaging/openwrt/README.md#manual-upgrade-from-an-older-cortex-a53-apk)保留已有网络组件。\n\n" +
	"安装后进入 LuCI 的「服务 → Hyper」。mihomo 内核需单独选择版本，离线时可导入对应架构的原始发行包。\n"

// AssetRevision reads the package revision from the IPK installers in dir.
func AssetRevision(version, dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	prefix := Name + "_" + version + "-"
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok {
			continue
		}
		rev, _, ok := strings.Cut(rest, "_")
		if n, err := strconv.Atoi(rev); ok && err == nil && n > 0 && strconv.Itoa(n) == rev {
			return n, nil
		}
	}
	return 0, errors.New("no installer carries a package revision")
}
