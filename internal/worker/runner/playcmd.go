package runner

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/noixdev/pulsix/internal/worker"
	"github.com/noixdev/pulsix/internal/worker/supervisor"
)

// newPlayCmd builds the *exec.Cmd of a program that runs as the play does:
// ansible-runner (newRunnerCmd, whose comment carries the whole argument)
// and ansible-galaxy (Galaxy). binVar names the variable that chose bin,
// for the error when it cannot be resolved; dir is the working directory,
// which neither the setuid helper nor the supervisor changes.
//
//	no supervisor, no play uid: <bin> <args…>
//	supervisor:                 <supervisor> __pulsix-play-supervisor --grace <grace> -- <abs bin> <args…>
//	play uid:                   <helper> <uid> <supervisor or abs bin> …
//
// Setpgid is unconditional: the cancel path signals the group.
func newPlayCmd(bin, binVar string, args []string, cfg worker.Config, dir string, env []string, grace time.Duration) (*exec.Cmd, error) {
	name, argv := bin, args
	if sup := cfg.PlaySupervisor; sup != "" || cfg.PlayUID != 0 {
		abs, err := resolveRunnerBin(bin, binVar)
		if err != nil {
			return nil, err
		}
		name, argv = abs, args
		if sup != "" {
			name, argv = sup, supervisor.Command(grace, abs, args)
		}
	}
	if uid := cfg.PlayUID; uid != 0 {
		helper := cfg.SetuidHelper
		if helper == "" {
			helper = defaultSetuidHelper
		}
		name, argv = helper, append([]string{strconv.FormatUint(uint64(uid), 10), name}, argv...)
	}
	cmd := exec.Command(name, argv...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, nil
}
