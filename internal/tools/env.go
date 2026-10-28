package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// Environment decides where bash and start_process commands run.
type Environment interface {
	// Command returns an unstarted `bash -c command` that runs in root.
	// Cancelling ctx must kill the command and everything it spawned.
	Command(ctx context.Context, root, command string) *exec.Cmd
	// Exec returns an unstarted command that runs argv directly, without a
	// shell, in root. argv[0] is looked up on the environment's PATH.
	// Cancelling ctx must kill the command and everything it spawned.
	Exec(ctx context.Context, root string, argv []string) *exec.Cmd
}

func environment(env Environment) Environment {
	if env == nil {
		return Local{}
	}
	return env
}

// Local runs commands on the host, confined by Sandbox if it is non-nil.
type Local struct {
	Sandbox *Sandbox
	// CommandEnv, when non-nil, is the whole environment commands get;
	// nil inherits tinyagent's.
	CommandEnv *CommandEnv
}

func (l Local) Command(ctx context.Context, root, command string) *exec.Cmd {
	if l.Sandbox == nil {
		return l.command(ctx, root, exec.CommandContext(ctx, "bash", "-c", command))
	}
	return l.command(ctx, root, nil, bashPath(), "-c", command)
}

// Exec runs argv without a shell. Unsandboxed, argv[0] is looked up on the
// command_env PATH (tinyagent's own without a policy); sandboxed, the
// sandbox wrapper looks it up on the PATH the command gets.
func (l Local) Exec(ctx context.Context, root string, argv []string) *exec.Cmd {
	if l.Sandbox != nil {
		return l.command(ctx, root, nil, argv...)
	}
	if l.CommandEnv == nil || strings.ContainsAny(argv[0], `/\`) {
		return l.command(ctx, root, exec.CommandContext(ctx, argv[0], argv[1:]...))
	}
	path, err := lookPathIn(argv[0], l.CommandEnv.Vars)
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Args[0] = argv[0]
	if err != nil {
		cmd.Err = err
	}
	return l.command(ctx, root, cmd)
}

// command finishes cmd for root: environment, directory and process group.
// A nil cmd is argv wrapped in the sandbox.
func (l Local) command(ctx context.Context, root string, cmd *exec.Cmd, argv ...string) *exec.Cmd {
	if cmd != nil {
		if l.CommandEnv != nil {
			cmd.Env = append([]string{}, l.CommandEnv.Vars...)
		}
	} else {
		cmd = l.Sandbox.wrap(ctx, argv)
		env := os.Environ()
		if l.CommandEnv != nil {
			env = append([]string{}, l.CommandEnv.Vars...)
		}
		_, _, proxyEnv := l.Sandbox.netState()
		cmd.Env = append(append(env, "TMPDIR="+l.Sandbox.Tmp), proxyEnv...)
	}
	cmd.Dir = root
	setProcessGroup(cmd)
	return cmd
}

// TakeNetworkDenials returns the requests the sandbox allowlist proxy
// refused since the last call.
func (l Local) TakeNetworkDenials() []sandboxnet.Denial {
	return l.Sandbox.TakeNetworkDenials()
}

// ContainerLabel marks containers started by tinyagent.
const ContainerLabel = "tinyagent"

// containerMarker is set in every command run in a container, so its
// processes (which inherit it) can be found and killed there.
const containerMarker = "TINYAGENT_EXEC"

// containerKill kills every process in the container whose environment holds
// containerMarker=$1. Killing the host-side exec client alone leaves them
// running.
const containerKill = `for p in /proc/[0-9]*; do
  tr '\0' '\n' < "$p/environ" 2>/dev/null | grep -qx "` + containerMarker + `=$1" && kill -9 "${p#/proc/}" 2>/dev/null
done; exit 0`

// Container runs commands with `<runtime> exec` in one container per run,
// with the workspace bind-mounted at the same path.
type Container struct {
	Runtime string // docker or podman
	Image   string
	ID      string
	Mount   string // host directory mounted at the same path
	// Extra are additional directories mounted at the same paths; read
	// mode ones are mounted read-only.
	Extra []ExtraDir
	// lockdown is the untrusted-content lockdown network switch, nil
	// unless PrepareLockdownNetwork set it (see lockdown_container.go).
	lockdown *containerLockdown
}

// StartContainer checks that runtime is usable and starts a container from
// image with root mounted at the same path, plus each additional directory
// in extra at its own path (read-only for read mode). It fails rather than
// falling back to the host. runArgs are extra `run` arguments, after the
// defaults.
func StartContainer(ctx context.Context, rt, image string, runArgs []string, root string, extra []ExtraDir) (*Container, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("container: not supported on Windows")
	}
	if rt == "" {
		rt = "docker"
	}
	if image == "" {
		return nil, errors.New("container: no image; set container.image in the config")
	}
	if _, err := exec.LookPath(rt); err != nil {
		return nil, fmt.Errorf("container: runtime %q not found in PATH; install it or set container.runtime", rt)
	}
	args := containerRunArgs(image, runArgs, root, extra)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, rt, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("container: %s run %s failed (is the daemon running and the image available?): %s", rt, image, msg)
	}
	id := strings.TrimSpace(stdout.String())
	if i := strings.LastIndexByte(id, '\n'); i >= 0 { // pull progress may precede the id
		id = strings.TrimSpace(id[i+1:])
	}
	if id == "" {
		return nil, fmt.Errorf("container: %s run printed no container id", rt)
	}
	return &Container{Runtime: rt, Image: image, ID: id, Mount: root, Extra: extra}, nil
}

// containerRunArgs builds the `run` arguments for StartContainer.
func containerRunArgs(image string, runArgs []string, root string, extra []ExtraDir) []string {
	args := []string{"run", "-d", "--rm", "--init", "--label", ContainerLabel + "=1",
		"-v", root + ":" + root}
	for _, d := range extra {
		v := d.Path + ":" + d.Path
		if d.Mode != DirWrite {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	args = append(args, "-w", root,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-e", "HOME=/tmp")
	args = append(args, runArgs...)
	return append(args, image, "sleep", "infinity")
}

func (c *Container) Command(ctx context.Context, root, command string) *exec.Cmd {
	return c.Exec(ctx, root, []string{"bash", "-c", command})
}

// Exec runs argv in the container without a shell; the container's PATH
// finds argv[0].
func (c *Container) Exec(ctx context.Context, root string, argv []string) *exec.Cmd {
	token := newToken()
	proxyEnv, lockErr := c.lockdownErr()
	args := []string{"exec", "-w", root, "-e", containerMarker + "=" + token}
	for _, v := range proxyEnv {
		args = append(args, "-e", v)
	}
	args = append(append(args, c.ID), argv...)
	cmd := exec.CommandContext(ctx, c.Runtime, args...)
	cmd.Dir = root
	setProcessGroup(cmd)
	if lockErr != nil {
		cmd.Err = lockErr
	}
	hostCancel := cmd.Cancel
	cmd.Cancel = func() error {
		c.kill(token)
		if hostCancel != nil {
			return hostCancel()
		}
		return cmd.Process.Kill()
	}
	return cmd
}

// kill kills the processes of the command marked token inside the container.
// It runs as the container's own user, not root: reading another user's
// /proc/<pid>/environ needs CAP_SYS_PTRACE, which containers lack by default,
// so a root scan silently matches nothing. The commands all run as the
// container user, so that user can both read their environ and kill them.
func (c *Container) kill(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exec.CommandContext(ctx, c.Runtime, "exec", c.ID, "sh", "-c", containerKill, "sh", token).Run()
}

// Close stops and removes the container. It is safe on a nil Container.
func (c *Container) Close() error {
	if c == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.Runtime, "rm", "-f", c.ID).CombinedOutput()
	if err != nil {
		err = fmt.Errorf("container: removing %s: %v: %s", c.ID, err, strings.TrimSpace(string(out)))
	}
	return errors.Join(err, c.closeLockdownProxy())
}

// Describe is the one-line summary printed at startup.
func (c *Container) Describe() string {
	s := fmt.Sprintf("environment: container (%s, image=%s); mount: %s", c.Runtime, c.Image, c.Mount)
	for _, d := range c.Extra {
		s += fmt.Sprintf(", %s (%s)", d.Path, d.Mode)
	}
	return s
}

// lookPathIn finds file in the absolute directories of the PATH in env
// (KEY=VALUE entries); relative entries are skipped, as exec skips ".".
func lookPathIn(file string, env []string) (string, error) {
	path := ""
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); k == "PATH" || (runtime.GOOS == "windows" && strings.EqualFold(k, "PATH")) {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		if p, err := exec.LookPath(filepath.Join(dir, file)); err == nil {
			return p, nil
		}
	}
	return file, fmt.Errorf("executable %q not found in the command environment's PATH", file)
}

func newToken() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
