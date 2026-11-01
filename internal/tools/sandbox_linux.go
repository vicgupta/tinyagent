//go:build linux

package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Landlock syscall numbers are the same on every architecture.
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446

	landlockCreateVersion   = 1 << 0
	landlockRulePathBeneath = 1
	landlockRuleNetPort     = 2
	prSetNoNewPrivs         = 38

	// Filesystem rights that modify the filesystem.
	accWriteFile  = 1 << 1
	accRemoveDir  = 1 << 4
	accRemoveFile = 1 << 5
	accMakeChar   = 1 << 6
	accMakeDir    = 1 << 7
	accMakeReg    = 1 << 8
	accMakeSock   = 1 << 9
	accMakeFifo   = 1 << 10
	accMakeBlock  = 1 << 11
	accMakeSym    = 1 << 12
	accRefer      = 1 << 13
	accTruncate   = 1 << 14

	accWriteAll = accWriteFile | accRemoveDir | accRemoveFile | accMakeChar | accMakeDir |
		accMakeReg | accMakeSock | accMakeFifo | accMakeBlock | accMakeSym | accRefer | accTruncate
	// accFileOnly is what may be granted on a non-directory.
	accFileOnly = accWriteFile | accTruncate

	// Network rights (ABI 4+).
	netBindTCP    = 1 << 0
	netConnectTCP = 1 << 1
)

// Writes need ABI 3 (truncate and refer are otherwise unconfinable or always
// denied); denying TCP needs ABI 4.
const (
	minABIWrite = 3
	minABINet   = 4
)

func landlockABI() (int, error) {
	r, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateVersion)
	if errno != 0 {
		return 0, errno
	}
	return int(r), nil
}

func probeSandbox(network, proxied bool) (string, error) {
	abi, err := landlockABI()
	if err != nil {
		return "", fmt.Errorf("sandbox: Landlock is unavailable (%v); it needs Linux 6.2+ with Landlock enabled in the kernel's LSM list. Run without --sandbox to skip it", err)
	}
	if abi < minABIWrite {
		return "", fmt.Errorf("sandbox: Landlock ABI %d is too old to confine writes (need %d, Linux 6.2+); run without --sandbox", abi, minABIWrite)
	}
	if !network && abi < minABINet {
		return "", fmt.Errorf("sandbox: Landlock ABI %d cannot deny network (need %d, Linux 6.7+); use --sandbox-network to allow it or run without --sandbox", abi, minABINet)
	}
	backend := fmt.Sprintf("landlock (ABI %d)", abi)
	if proxied {
		if err := probeNamespaces(); err != nil {
			return "", err
		}
		backend += " + network namespace"
	}
	return backend, nil
}

// proxyPort is where each command's network namespace forwards to the
// allowlist proxy. Every command gets a fresh namespace, so it is always free.
const proxyPort = 3128

// namespaceAttr runs the helper in new user and network namespaces, as the
// same uid and gid, keeping CAP_NET_ADMIN only to bring up loopback there.
func namespaceAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
		AmbientCaps: []uintptr{unix.CAP_NET_ADMIN},
	}
}

func helperPath() string {
	self, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return self
}

// probeNamespaces checks that the helper can set up the network namespace
// the allowlist mode needs.
func probeNamespaces() error {
	cmd := exec.Command(helperPath(), SandboxHelperArg, "net-probe")
	cmd.SysProcAttr = namespaceAttr()
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		err = fmt.Errorf("%v: %s", err, msg)
	}
	return fmt.Errorf("sandbox: sandbox_allowed_domains needs unprivileged user and network namespaces, which this host refused (%v). "+
		"On Ubuntu 23.10+ AppArmor restricts them (sysctl kernel.apparmor_restrict_unprivileged_userns); elsewhere check kernel.unprivileged_userns_clone and user.max_user_namespaces. "+
		"Run without sandbox_allowed_domains to skip it", err)
}

func (s *Sandbox) listenProxy() (net.Listener, string, error) {
	dir, err := os.MkdirTemp("", "tinyagent-proxy-")
	if err != nil {
		return nil, "", err
	}
	s.proxyDir = dir
	ln, err := net.Listen("unix", filepath.Join(dir, "proxy.sock"))
	if err != nil {
		return nil, "", err
	}
	return ln, "http://127.0.0.1:" + strconv.Itoa(proxyPort), nil
}

// wrap re-executes tinyagent as a helper that applies Landlock to itself and
// execs bash, so only the command (not tinyagent) is confined. In allowlist
// mode the helper starts in its own network namespace, which has only
// loopback, and forwards proxyPort there to the proxy's Unix socket.
func (s *Sandbox) wrap(ctx context.Context, argv []string) *exec.Cmd {
	if s.bwrap != "" {
		return s.wrapBwrap(ctx, argv)
	}
	network, proxy, _ := s.netState()
	mode := "net-deny"
	if network {
		mode = "net-allow"
	} else if proxy != nil {
		mode = fmt.Sprintf("net-proxy=%d:%s", proxyPort, proxy.Addr().String())
	}
	args := append([]string{SandboxHelperArg, mode}, s.Writable...)
	args = append(append(args, "--"), argv...)
	cmd := exec.CommandContext(ctx, helperPath(), args...)
	if proxy != nil {
		cmd.SysProcAttr = namespaceAttr()
	}
	return cmd
}

// RunSandboxHelper applies the Landlock policy in args (see wrap) to the
// current thread and execs the command. It only returns on failure. The
// helper modes are net-allow, net-deny and net-port=<port> (connect only to
// that port); net-proxy=<port>:<socket> (see runProxied); and net-probe.
func RunSandboxHelper(args []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		return 126
	}
	if len(args) == 1 && args[0] == "net-probe" {
		if err := probeLoopback(); err != nil {
			return fail(err)
		}
		return 0
	}
	if len(args) < 3 {
		return fail(fmt.Errorf("bad helper arguments"))
	}
	var writable, argv []string
	for i, a := range args[1:] {
		if a == "--" {
			argv = args[i+2:]
			break
		}
		writable = append(writable, a)
	}
	if len(argv) == 0 {
		return fail(fmt.Errorf("no command"))
	}
	pol := netPolicy{allow: args[0] == "net-allow"}
	if spec, ok := strings.CutPrefix(args[0], "net-proxy="); ok {
		port, sock, _ := strings.Cut(spec, ":")
		p, err := strconv.Atoi(port)
		if err != nil || sock == "" {
			return fail(fmt.Errorf("bad helper mode %q", args[0]))
		}
		return runProxied(p, sock, writable, argv)
	}
	if port, ok := strings.CutPrefix(args[0], "net-port="); ok {
		p, err := strconv.Atoi(port)
		if err != nil {
			return fail(fmt.Errorf("bad helper mode %q", args[0]))
		}
		pol.port = p
	}
	bash, err := exec.LookPath(argv[0])
	if err != nil {
		return fail(err)
	}
	// Landlock and no_new_privs apply to one thread; exec from that thread.
	runtime.LockOSThread()
	if err := restrictSelf(writable, pol); err != nil {
		return fail(err)
	}
	err = syscall.Exec(bash, argv, os.Environ())
	return fail(err)
}

// netPolicy is the Landlock TCP policy: allow everything, or deny all but
// connecting to port (0 for none).
type netPolicy struct {
	allow bool
	port  int
}

// loopbackUp brings up lo, which starts down in a new network namespace.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("reading loopback flags: %w", err)
	}
	flags := ifr.Uint16()
	if flags&unix.IFF_UP != 0 {
		return nil
	}
	ifr.SetUint16(flags | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("bringing up loopback: %w", err)
	}
	return nil
}

func probeLoopback() error {
	if err := loopbackUp(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	return ln.Close()
}

// runProxied runs in the command's own network namespace. It forwards
// 127.0.0.1:port to the allowlist proxy's Unix socket, then runs the
// command through a net-port helper (Landlock allowing TCP connect only to
// port) and exits as the command did.
func runProxied(port int, sock string, writable, argv []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		return 126
	}
	if err := loopbackUp(); err != nil {
		return fail(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return fail(err)
	}
	go forward(ln, sock)

	args := append([]string{SandboxHelperArg, "net-port=" + strconv.Itoa(port)}, writable...)
	args = append(append(args, "--"), argv...)
	cmd := exec.Command(helperPath(), args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	go func() {
		for sig := range sigs {
			cmd.Process.Signal(sig)
		}
	}()
	err = cmd.Wait()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return fail(err)
	}
	ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if ws.Signaled() {
		signal.Reset(ws.Signal())
		syscall.Kill(os.Getpid(), ws.Signal())
		return 128 + int(ws.Signal())
	}
	return ws.ExitStatus()
}

func forward(ln net.Listener, sock string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			u, err := net.Dial("unix", sock)
			if err != nil {
				return
			}
			defer u.Close()
			done := make(chan struct{})
			go func() {
				io.Copy(u, c)
				u.(*net.UnixConn).CloseWrite()
				close(done)
			}()
			io.Copy(c, u)
			c.(*net.TCPConn).CloseWrite()
			<-done
		}()
	}
}

func restrictSelf(writable []string, pol netPolicy) error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	if abi < minABIWrite || (!pol.allow && abi < minABINet) {
		//lint:ignore ST1005 Landlock is a proper noun
		return fmt.Errorf("Landlock ABI %d too old", abi)
	}
	var attr struct{ fs, net, scoped uint64 }
	attr.fs = accWriteAll
	if !pol.allow {
		attr.net = netBindTCP | netConnectTCP
	}
	fd, _, errno := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer syscall.Close(int(fd))
	if pol.port > 0 {
		rule := struct{ access, port uint64 }{netConnectTCP, uint64(pol.port)}
		if _, _, errno := syscall.Syscall6(sysLandlockAddRule, fd, landlockRuleNetPort, uintptr(unsafe.Pointer(&rule)), 0, 0, 0); errno != 0 {
			return fmt.Errorf("landlock_add_rule port %d: %w", pol.port, errno)
		}
		// Drop the CAP_NET_ADMIN the namespace setup kept, so the command
		// starts without capabilities.
		if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
			return fmt.Errorf("clearing ambient capabilities: %w", err)
		}
	}

	allow := func(path string, optional bool) error {
		pfd, err := syscall.Open(path, unix.O_PATH|syscall.O_CLOEXEC, 0)
		if err != nil {
			if optional {
				return nil
			}
			return fmt.Errorf("%s: %w", path, err)
		}
		defer syscall.Close(pfd)
		var st syscall.Stat_t
		if err := syscall.Fstat(pfd, &st); err != nil {
			return err
		}
		access := uint64(accWriteAll)
		if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
			access = accFileOnly
		}
		var rule struct {
			access uint64
			fd     int32
		}
		rule.access, rule.fd = access, int32(pfd)
		_, _, errno := syscall.Syscall6(sysLandlockAddRule, fd, landlockRulePathBeneath, uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
		if errno != 0 {
			return fmt.Errorf("landlock_add_rule %s: %w", path, errno)
		}
		return nil
	}
	for _, w := range writable {
		if err := allow(w, false); err != nil {
			return err
		}
	}
	for _, d := range devFiles {
		if err := allow(d, true); err != nil {
			return err
		}
	}
	if err := unix.Prctl(prSetNoNewPrivs, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("prctl(NO_NEW_PRIVS): %w", err)
	}
	if _, _, errno := syscall.Syscall(sysLandlockRestrictSelf, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}
