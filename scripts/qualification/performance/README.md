# Physical router measurements

Use one 256 MiB router per target, a separate LAN load generator and the same WAN echo endpoint for all three trials. Record the actual CPU model and floating-point ABI. Run the same forwarding method, firmware, core binary and SHA-256 and fixture of 500 nodes, 20 groups, 50,000 rules and 100 concurrent connections. Record a full 24 hours for each of bare mihomo, managed with connection recording off and managed with recording on.

Place `device.json` and three directories named `bare`, `managed-off` and `managed-on` in the result directory. In `device.json`, include `architecture` (`armv7` or `mipsle-softfloat`), `physical_device` (boolean), `ram_mib`, `nodes`, `groups`, `rules` and `concurrent_connections`. Run `sample.sh 5` on the router and stream its stdout over SSH into the trial's `router.csv`. This samples process PSS, falling back to RSS when the kernel lacks PSS accounting. CPU time is expressed as a percentage of all CPU ticks. A gap or counter discontinuity invalidates the trial.

For each trial store `workload.json` from the load generator. Include `device`, `firmware`, `asset_sha256`, `workload_sha256`, `method`, TCP goodput as `tcp_bits_per_second`, at least 1,000 independent connection durations in milliseconds in `connect_ms`, `udp_sent`, `udp_received`, and `oom_count` from the router kernel log. Include the load generator log, fixed seed, duration and rule reload observations alongside the measurements.

```sh
go run ./scripts/qualification/performance/evaluate /path/to/trials armv7
```

The evaluator reports memory, CPU time, TCP goodput, UDP loss and P99 setup time. It exits unsuccessfully for incomplete trials or a missed budget. Run the tests on isolated physical equipment with laboratory subscriptions.
