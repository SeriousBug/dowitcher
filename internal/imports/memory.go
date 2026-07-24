package imports

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// availableMemoryBytes reports the memory ceiling this process should size its
// work against, and whether that ceiling is known.
//
// It prefers the cgroup limit over host RAM: dowitcher ships as a distroless
// container, and the cgroup limit is what the kernel actually OOM-kills against,
// so that is the number worth staying under. Only when no cgroup limit is set
// does it fall back to MemAvailable from /proc — the free memory the host can
// hand out without swapping. Non-Linux hosts (a dev machine) have none of these
// files and report unknown, leaving the caller to a conservative default rather
// than a guess.
func availableMemoryBytes() (uint64, bool) {
	if v, ok := cgroupMemoryLimit(); ok {
		return v, true
	}
	if v, ok := procMemAvailable(); ok {
		return v, true
	}
	return 0, false
}

// cgroupUnlimited is the threshold above which a cgroup limit is treated as
// "no limit". cgroup v1 encodes unlimited as a value near max int64 rather than
// a sentinel string, and no real container is given exabytes, so anything this
// large is not a ceiling worth sizing against.
const cgroupUnlimited = uint64(1) << 62

func cgroupMemoryLimit() (uint64, bool) {
	for _, p := range []string{
		"/sys/fs/cgroup/memory.max",                   // cgroup v2
		"/sys/fs/cgroup/memory/memory.limit_in_bytes", // cgroup v1
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(b))
		if s == "max" {
			// cgroup v2's explicit "no limit"; fall through to host RAM.
			return 0, false
		}
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			continue
		}
		if v >= cgroupUnlimited {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

func procMemAvailable() (uint64, bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, false
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}
