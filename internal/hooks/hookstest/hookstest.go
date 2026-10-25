// Package hookstest provides a portable hook program for tests: the test
// binary re-executes itself, so tests need no shell or jq.
package hookstest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
)

const marker = "tinyagent-hook-helper"

// Run acts as the hook and exits when the test binary was started by a hook
// from Hook. Call it first in TestMain.
func Run() {
	if len(os.Args) < 3 || os.Args[1] != marker {
		return
	}
	os.Exit(helper(os.Args[2], os.Args[3:]))
}

// Hook returns a hook entry running mode with args:
//
//	stdout TEXT       print TEXT, exit 0
//	exit2 TEXT        print TEXT to stderr, exit 2
//	exit N            exit with status N
//	sleep [PIDFILE]   write the pid to PIDFILE if given, then sleep
//	stdin FILE        copy stdin to FILE, exit 0
//	env FILE          write the environment to FILE, exit 0
//	log FILE TAG      append TAG and a newline to FILE, exit 0
//	gate DIR N TEXT   save stdin as DIR/<run>.json; print TEXT to stderr
//	                  and exit 2 for the first N runs, then exit 0
func Hook(t testing.TB, match, mode string, args ...string) config.Hook {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return config.Hook{Match: match, Command: exe, Args: append([]string{marker, mode}, args...)}
}

func helper(mode string, args []string) int {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch mode {
	case "stdout":
		fmt.Print(arg(0))
	case "exit2":
		fmt.Fprint(os.Stderr, arg(0))
		return 2
	case "exit":
		n, _ := strconv.Atoi(arg(0))
		return n
	case "sleep":
		if arg(0) != "" {
			os.WriteFile(arg(0), []byte(strconv.Itoa(os.Getpid())), 0o644)
		}
		time.Sleep(time.Minute)
	case "stdin":
		b, _ := io.ReadAll(os.Stdin)
		os.WriteFile(arg(0), b, 0o644)
	case "env":
		os.WriteFile(arg(0), []byte(strings.Join(os.Environ(), "\n")), 0o644)
	case "log":
		f, err := os.OpenFile(arg(0), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return 1
		}
		defer f.Close()
		fmt.Fprintln(f, arg(1))
	case "gate":
		entries, err := os.ReadDir(arg(0))
		if err != nil {
			return 1
		}
		run := len(entries)
		b, _ := io.ReadAll(os.Stdin)
		if err := os.WriteFile(filepath.Join(arg(0), strconv.Itoa(run)+".json"), b, 0o644); err != nil {
			return 1
		}
		if n, _ := strconv.Atoi(arg(1)); run < n {
			fmt.Fprint(os.Stderr, arg(2))
			return 2
		}
	default:
		return 1
	}
	return 0
}
