# Guest firmware checks

The guest boots the official OpenWrt ARM64 kernel and ext4 root filesystem under QEMU. It runs procd, ubus, dnsmasq and the selected OpenWrt firewall with the image's kernel and package manager. The host needs Go, Docker and internet access for the official artifacts. The router software runs in an isolated guest.

For each release, fetch the matching network prerequisites and run the serial fixture.

```sh
version=25.12.6
go run ./scripts/qualification/firmware/packages "$version" /tmp/fw-pkgs-"$version"
go run ./scripts/qualification/firmware/run "$version" \
  --cache /tmp/fw-images --packages /tmp/fw-pkgs-"$version" \
  --log /tmp/hyper-"$version".log --report /tmp/hyper-"$version".json
```

Both commands support `--docker-context`. Use `23.05.6`, `24.10.8` or `25.12.6`. The runner verifies official image SHA-256 values and stages the project network helper, UCI defaults and fw4 includes into a disposable copy of the image. On APK targets the guest validates the packages against the staged official repository indexes.

To qualify firewall3 on any of the three releases, pass `--firewall fw3` to both commands, with separate prerequisites and report paths. The disposable guest installs the matching official legacy xtables packages and exercises the running firewall3 service.

To test the release candidate itself, add `--network-package /path/to/luci-app-hyper_1.0.0-1_aarch64_generic.ipk --disk-mib 256` to the standard network run. For 25.12 use the matching APK. The host uses OpenSSL to generate a disposable certificate. The guest verifies the installer checksum, installs it with its package manager, moves its own clock outside and back into the certificate validity interval and checks that the failed HTTPS subscription refresh recovers after an NTP hotplug event while the proxy remains stopped. It then runs the network checks using the installed helpers. The report records the verified digest in `results.installed_package_sha256`. Compare this field with the candidate's SHA256SUMS when selecting evidence for a build.

The default fixture checks policy routing and local IPv4/IPv6 management through TProxy and TUN takeovers, firewall reloads and release. It checks that local DNS names and unrelated firewall rules survive. The ordinary upstream resolver and the test core resolver return different answers so the guest can verify both takeover and restoration. This isolates the production network helper from core lifetime and signing tests. To install additional test components, use `--guest-overlay DIR --guest-script FILE --disk-mib 768`. The replacement script prints `HYPER_RESULT daemon_lifecycle=pass` followed by `HYPER_QUALIFICATION_END` on success.

The packet-level fixture runs the verified Mihomo ARM64 binary and dnsmasq in isolated network namespaces. It queries the gateway and two explicitly configured DNS servers over IPv4 and IPv6 with both TCP and UDP. It checks LAN, guest and unmanaged networks across firewall reload, normal stop and guard recovery. The Docker VM kernel needs TUN, TProxy and the selected firewall backend.

```sh
HYPER_TEST_FIREWALL=fw3 scripts/kernel/check-network.sh "$MIHOMO_ARM64_ARCHIVE" "$VERIFIED_SHA256"
HYPER_TEST_FIREWALL=fw4 scripts/kernel/check-network.sh "$MIHOMO_ARM64_ARCHIVE" "$VERIFIED_SHA256"
```

## Original release installation and recovery

The guest stages an ARM64 Mihomo archive in a temporary signing domain. It exercises candidate rejection, daemon startup, local DNS, rollback and cleanup. The power-cut variant reboots the same disk with an unconfirmed transaction.

```sh
scripts/kernel/check-daemon.sh "$MIHOMO_ARM64_ARCHIVE" "$VERIFIED_SHA256" 25.12.6 \
  --cache /tmp/fw-images --packages /tmp/fw-pkgs-25.12.6 --log /tmp/mihomo-switch.log \
  --report /tmp/mihomo-switch.json --power-cut
```
