#!/bin/sh
# Isolated ARM64 userland tests. The upstream archive is used only to verify
# the restricted execution ABI; it is not admitted to production trust.
set -eu
archive=${1:?pass the Mihomo Linux ARM64 release archive}
tag=${2:?pass the matching release tag}
expected=${3:?pass the independently verified archive SHA-256}
case "$tag" in v[0-9]*);; *) exit 2;; esac
dir=mihomo-${tag#v}-linux-arm64
version=$tag
case "$tag" in v0.0.0-alpha.*) version=alpha-${tag#v0.0.0-alpha.};; esac
work=$(mktemp -d)
cid=
cleanup() {
    if [ -n "$cid" ]; then docker rm -f "$cid" >/dev/null 2>&1 || :; fi
    rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
sum=$(openssl dgst -sha256 < "$archive" | awk '{print $NF}')
[ "$sum" = "$expected" ] || { echo "unexpected qualification fixture" >&2; exit 1; }
case "$(basename "$archive")" in
 "$dir.tar.gz") tar -xOzf "$archive" "$dir/mihomo" > "$work/mihomo" ;;
 "mihomo-linux-arm64-$version.gz") gzip -dc "$archive" > "$work/mihomo" ;;
 *) exit 2 ;;
esac
chmod 755 "$work/mihomo"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$work/manager" ./cmd/hyper-kernel
# The running core is root in base-files' nogroup group (65534) alone with every capability
# and 1000000 open files, as hyperd starts it: router.test runs that launch code on this
# userland, from procd's default open-file limits (1024 soft, 4096 hard). The restricted
# checks (exec-restricted) keep the same identity but hold no capability, under no_new_privs.
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c -trimpath -o "$work/router.test" ./internal/router
cat > "$work/probe.go" <<'PROBE'
package main
import("fmt"; "os"; "strings"; "syscall")
func main() {
 b,e:=os.ReadFile("/proc/self/status");if e!=nil{panic(e)}
 found:=0
 for _,s:=range strings.Split(string(b),"\n"){f:=strings.Fields(s);if len(f)<1{continue}
 switch f[0] {
 case "Uid:": for _,n:=range f[1:]{if n!="0"{panic(s)}};found++
 case "Gid:": for _,n:=range f[1:]{if n!="65534"{panic(s)}};found++
 case "Groups:":if len(f)!=1{panic(s)};found++
 case "CapBnd:","CapEff:","CapPrm:","CapAmb:","CapInh:":if len(f)<2||f[1]!="0000000000000000"{panic(s)};found++
 case "NoNewPrivs:":if len(f)<2||f[1]!="1"{panic(s)};found++
 default:continue
 };fmt.Println(s)}
 if found!=9{panic("missing privilege evidence")}
 // Root without CAP_DAC_OVERRIDE, CAP_CHOWN or CAP_SETUID: another user's file stays closed
 // and the check cannot give files away or leave its identity.
 if _,e=os.ReadFile("/tmp/foreign");e==nil{panic("check read another user's file")}
 if e=os.Chown("/tmp/probe",65534,65534);e==nil{panic("check changed a file's owner")}
 if e=syscall.Setuid(65534);e==nil{panic("check changed its user")}
}
PROBE
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o "$work/probe" "$work/probe.go"
for release in 23.05.6 24.10.8 25.12.6; do
    cid=$(docker create --env CORE_TAG="$version" --cap-add NET_ADMIN --cap-add SYS_RESOURCE --ulimit nofile=1024:4096 --network none --platform linux/arm64 "hyper-test/openwrt:$release-arm64" sh -ec '
        test "$(awk -F: "\$1 == \"nogroup\" { print \$3 }" /etc/group)" = 65534
        echo private >/tmp/foreign && chown 4242:4242 /tmp/foreign && chmod 0600 /tmp/foreign
        mkdir -p /etc/config
        mkdir -p /var/run/hyper/mihomo
        echo "mode: direct" >/tmp/config.yaml
        /tmp/manager exec-restricted /tmp/probe -v
        HYPER_CORE_FIXTURE=1 /tmp/router.test -test.run "^TestCoreLaunchHoldsEveryCapability\$" -test.v >/tmp/launch.log || { cat /tmp/launch.log; exit 1; }
        grep -q "^--- PASS: TestCoreLaunchHoldsEveryCapability " /tmp/launch.log || { cat /tmp/launch.log; exit 1; }
        /tmp/manager exec-restricted /tmp/mihomo -v | grep -F "Mihomo Meta $CORE_TAG linux arm64 with "
        /tmp/manager exec-restricted /tmp/mihomo -t -d /var/run/hyper/mihomo -f /tmp/config.yaml
    ')
    for prog in probe mihomo manager router.test; do docker cp "$work/$prog" "$cid:/tmp/$prog"; done
    docker start -a "$cid"
    [ "$(docker inspect -f '{{.State.ExitCode}}' "$cid")" = 0 ]
    docker rm "$cid" >/dev/null
    cid=
    echo "OpenWrt $release ARM64 restricted version/check and core launch passed"
done
