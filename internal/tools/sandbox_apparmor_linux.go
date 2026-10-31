//go:build linux

package tools

import (
	"fmt"
	"os"
	"strings"
)

// apparmorUsernsPath is the sysctl Ubuntu 23.10+ uses to restrict
// unprivileged user namespaces to AppArmor profiles that allow them.
// Tests replace it.
var apparmorUsernsPath = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"

// apparmorFix installs the AppArmor profile that lets bwrap create user
// namespaces. tinyagent never runs it or changes sysctls itself.
const apparmorFix = "sudo apt install apparmor-profiles apparmor-utils\n" +
	"  sudo install -m 0644 /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/bwrap-userns-restrict\n" +
	"  sudo apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict"

// apparmorRestrictsUserns reports whether the sysctl at path is set to 1.
func apparmorRestrictsUserns(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// bwrapProbeError explains a failed bwrap probe. When AppArmor restricts
// unprivileged user namespaces (Ubuntu 24.04's default) it names that and
// gives the commands that install bwrap's AppArmor profile.
func bwrapProbeError(err error, procPath string) error {
	if apparmorRestrictsUserns(procPath) {
		return fmt.Errorf("sandbox: bwrap can't create its sandbox (%v): AppArmor restricts unprivileged user namespaces "+
			"(kernel.apparmor_restrict_unprivileged_userns = 1, the Ubuntu 24.04 default). Allow bwrap with its AppArmor profile:\n  %s", err, apparmorFix)
	}
	return fmt.Errorf("sandbox: bwrap can't create its sandbox (%v); check that unprivileged user namespaces are enabled "+
		"(kernel.unprivileged_userns_clone, user.max_user_namespaces)", err)
}
