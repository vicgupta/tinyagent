//go:build !windows

package lsptest

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ReadPids waits for n pids in file.
func ReadPids(t testing.TB, file string, n int) []int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(file)
		var pids []int
		for _, f := range strings.Fields(string(b)) {
			if p, err := strconv.Atoi(f); err == nil {
				pids = append(pids, p)
			}
		}
		if len(pids) >= n || time.Now().After(deadline) {
			if len(pids) < n {
				t.Fatalf("pids = %v, want %d", pids, n)
			}
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// AssertGone fails unless every pid has exited.
func AssertGone(t testing.TB, pids []int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for _, p := range pids {
		for syscall.Kill(p, 0) == nil && !zombie(p) {
			if time.Now().After(deadline) {
				t.Errorf("process %d still running", p)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// zombie reports an exited process its parent has not reaped yet (Linux).
func zombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	return i >= 0 && i+2 < len(s) && s[i+2] == 'Z'
}
