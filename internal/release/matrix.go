package release

import "strings"

// Job is one version-matched SDK build / installation job.
type Job struct {
	Firmware string `json:"firmware"`
	Arch     string `json:"arch"`
}

// Matrix expands the published OpenWrt feeds; format "" returns both package formats.
func Matrix(format string) []Job {
	var jobs []Job
	for _, version := range []string{"23.05.6", "24.10.8", "25.12.6"} {
		if format != "" && strings.HasPrefix(version, "25.") != (format == "apk") {
			continue
		}
		pm := "ipk"
		if strings.HasPrefix(version, "25.") {
			pm = "apk"
		}
		for _, a := range Arches[pm] {
			if version == "23.05.6" && a == "loongarch64_generic" {
				continue // 23.05 has no LoongArch target.
			}
			jobs = append(jobs, Job{version, a})
		}
	}
	return jobs
}
