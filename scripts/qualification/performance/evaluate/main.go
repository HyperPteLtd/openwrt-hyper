// Command evaluate compares the three same-device trials by measurements supplied by the lab.
//
//	go run ./scripts/qualification/performance/evaluate ROOT armv7|mipsle-softfloat
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

var ram = map[string][3]float64{"mipsle-softfloat": {144, 148, 4}, "armv7": {160, 166, 6}}
var trials = []string{"bare", "managed-off", "managed-on"}

type trial struct {
	Identity      [5]string `json:"identity"`
	TCP           float64   `json:"tcp_bits_per_second"`
	ConnectP99    float64   `json:"connect_p99_ms"`
	PeakMiB       float64   `json:"peak_mib"`
	CPUPct        float64   `json:"cpu_pct"`
	UDPLoss       float64   `json:"udp_loss"`
	PeakTmpMiB    float64   `json:"peak_tmp_mib"`
	RSSUpperBound bool      `json:"rss_upper_bound"`
}

type workload struct {
	Device         string      `json:"device"`
	Firmware       string      `json:"firmware"`
	AssetSHA256    string      `json:"asset_sha256"`
	WorkloadSHA256 string      `json:"workload_sha256"`
	Method         string      `json:"method"`
	ConnectMS      []float64   `json:"connect_ms"`
	TCP            json.Number `json:"tcp_bits_per_second"`
	UDPSent        json.Number `json:"udp_sent"`
	UDPReceived    json.Number `json:"udp_received"`
	OOMCount       *int        `json:"oom_count"`
}

func readTrial(dir string) (trial, error) {
	var t trial
	raw, err := os.ReadFile(filepath.Join(dir, "workload.json"))
	if err != nil {
		return t, err
	}
	var stats workload
	if err := json.Unmarshal(raw, &stats); err != nil {
		return t, err
	}
	f, err := os.Open(filepath.Join(dir, "router.csv"))
	if err != nil {
		return t, err
	}
	records, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		return t, err
	}
	if len(records) < 3 {
		return t, errors.New("each trial requires at least 24h of router observations")
	}
	col := map[string]int{}
	for i, h := range records[0] {
		col[h] = i
	}
	num := func(row []string, key string) (float64, error) {
		i, ok := col[key]
		if !ok || i >= len(row) {
			return 0, fmt.Errorf("missing column %s", key)
		}
		return strconv.ParseFloat(row[i], 64)
	}
	rows := records[1:]
	first, err1 := num(rows[0], "uptime_seconds")
	last, err2 := num(rows[len(rows)-1], "uptime_seconds")
	if err := errors.Join(err1, err2); err != nil {
		return t, err
	}
	if last-first < 86400 {
		return t, errors.New("each trial requires at least 24h of router observations")
	}
	var busy, total float64
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		ua, _ := num(a, "uptime_seconds")
		ub, _ := num(b, "uptime_seconds")
		ca, e1 := num(a, "cpu_ticks")
		cb, e2 := num(b, "cpu_ticks")
		ta, e3 := num(a, "total_cpu_ticks")
		tb, e4 := num(b, "total_cpu_ticks")
		if err := errors.Join(e1, e2, e3, e4); err != nil {
			return t, err
		}
		elapsed, db, dt := ub-ua, cb-ca, tb-ta
		if !(0 < elapsed && elapsed <= 15) || !(0 <= db && db <= dt) || dt <= 0 {
			return t, errors.New("clock gap, restart, or invalid process accounting")
		}
		busy += db
		total += dt
	}
	peak, peakTmp := 0.0, 0.0
	for _, r := range rows {
		kib, err := num(r, "process_kib")
		if err != nil {
			return t, err
		}
		if kib <= 0 {
			return t, errors.New("missing core or invalid memory counters")
		}
		tmpKiB, err := num(r, "tmp_used_kib")
		if err != nil {
			return t, err
		}
		fallback, err := num(r, "rss_fallback")
		if err != nil {
			return t, err
		}
		peak, peakTmp = math.Max(peak, kib), math.Max(peakTmp, tmpKiB)
		t.RSSUpperBound = t.RSSUpperBound || fallback != 0
	}
	timings := slices.Clone(stats.ConnectMS)
	slices.Sort(timings)
	if len(timings) < 1000 || slices.ContainsFunc(timings, func(x float64) bool { return math.IsInf(x, 0) || math.IsNaN(x) || x <= 0 }) {
		return t, errors.New("expected at least 1000 measured connection durations")
	}
	throughput, e1 := stats.TCP.Float64()
	sent, e2 := stats.UDPSent.Int64()
	received, e3 := stats.UDPReceived.Int64()
	if errors.Join(e1, e2, e3) != nil || math.IsInf(throughput, 0) || math.IsNaN(throughput) || throughput <= 0 ||
		sent <= 0 || received < 0 || received > sent || stats.OOMCount == nil || *stats.OOMCount != 0 {
		return t, errors.New("invalid throughput, UDP counts, or OOM observed")
	}
	t.Identity = [5]string{stats.Device, stats.Firmware, stats.AssetSHA256, stats.WorkloadSHA256, stats.Method}
	t.TCP = throughput
	t.ConnectP99 = timings[int(math.Ceil(.99*float64(len(timings))))-1]
	t.PeakMiB, t.PeakTmpMiB = peak/1024, peakTmp/1024
	t.CPUPct = 100 * busy / total
	t.UDPLoss = float64(sent-received) / float64(sent)
	return t, nil
}

func evaluate(root, arch string) (map[string]any, bool, error) {
	raw, err := os.ReadFile(filepath.Join(root, "device.json"))
	if err != nil {
		return nil, false, err
	}
	var lab map[string]any
	if err := json.Unmarshal(raw, &lab); err != nil {
		return nil, false, err
	}
	if lab["architecture"] != arch || lab["physical_device"] != true || lab["ram_mib"] != 256.0 ||
		lab["nodes"] != 500.0 || lab["groups"] != 20.0 || lab["rules"] != 50000.0 || lab["concurrent_connections"] != 100.0 {
		return nil, false, errors.New("device or workload differs from the low-end qualification budget")
	}
	result := map[string]trial{}
	for _, name := range trials {
		if result[name], err = readTrial(filepath.Join(root, name)); err != nil {
			return nil, false, fmt.Errorf("%s: %w", name, err)
		}
	}
	bare, off, on := result["bare"], result["managed-off"], result["managed-on"]
	for _, v := range result {
		if v.Identity != bare.Identity {
			return nil, false, errors.New("all trials must use the same device, firmware, core, workload and interception method")
		}
	}
	budget := ram[arch]
	checks := map[string]bool{
		"peak_off":         off.PeakMiB <= budget[0],
		"peak_on":          on.PeakMiB <= budget[1],
		"recording_memory": on.PeakMiB-off.PeakMiB <= budget[2],
		"recording_cpu":    on.CPUPct-off.CPUPct <= 3,
	}
	for _, name := range trials[1:] {
		checks[name+"_throughput"] = result[name].TCP >= .95*bare.TCP
		checks[name+"_p99"] = result[name].ConnectP99 <= 1.1*bare.ConnectP99
	}
	passed := true
	for _, ok := range checks {
		passed = passed && ok
	}
	return map[string]any{"architecture": arch, "trials": result, "checks": checks, "passed": passed}, passed, nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: evaluate ROOT armv7|mipsle-softfloat")
		os.Exit(2)
	}
	if _, ok := ram[os.Args[2]]; !ok {
		fmt.Fprintln(os.Stderr, "evaluate: architecture must be armv7 or mipsle-softfloat")
		os.Exit(2)
	}
	report, passed, err := evaluate(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	out, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(out))
	if !passed {
		os.Exit(1)
	}
}
