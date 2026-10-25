//go:build linux

package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

func probeSandbox(network bool) (string, error) {
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
	return fmt.Sprintf("landlock (ABI %d)", abi), nil
}

// wrap re-executes tinyagent as a helper that applies Landlock to itself and
// execs bash, so only the command (not tinyagent) is confined.
func (s *Sandbox) wrap(ctx context.Context, command string) *exec.Cmd {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	mode := "net-deny"
	if s.Network {
		mode = "net-allow"
	}
	args := append([]string{SandboxHelperArg, mode}, s.Writable...)
	args = append(args, "--", bashPath(), "-c", command)
	return exec.CommandContext(ctx, self, args...)
}

// RunSandboxHelper applies the Landlock policy in args (see wrap) to the
// current thread and execs the command. It only returns on failure.
func RunSandboxHelper(args []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "sandbox:", err)
		return 126
	}
	if len(args) < 3 {
		return fail(fmt.Errorf("bad helper arguments"))
	}
	network := args[0] == "net-allow"
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
	bash, err := exec.LookPath(argv[0])
	if err != nil {
		return fail(err)
	}
	// Landlock and no_new_privs apply to one thread; exec from that thread.
	runtime.LockOSThread()
	if err := restrictSelf(writable, network); err != nil {
		return fail(err)
	}
	err = syscall.Exec(bash, argv, os.Environ())
	return fail(err)
}

func restrictSelf(writable []string, network bool) error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	if abi < minABIWrite || (!network && abi < minABINet) {
		return fmt.Errorf("Landlock ABI %d too old", abi)
	}
	var attr struct{ fs, net, scoped uint64 }
	attr.fs = accWriteAll
	if !network {
		attr.net = netBindTCP | netConnectTCP
	}
	fd, _, errno := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer syscall.Close(int(fd))

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
