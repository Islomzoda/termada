//go:build windows

package plugin

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// containPluginProcess assigns the plugin to a kill-on-close Job Object using
// the exact process handle (never a PID that could have been reused).
func configurePluginCommand(cmd *exec.Cmd) {}

func containPluginProcess(cmd *exec.Cmd) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	closeJob := func() { _ = windows.CloseHandle(job) }
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		closeJob()
		return nil, err
	}
	var assignErr error
	if err := cmd.Process.WithHandle(func(handle uintptr) {
		assignErr = windows.AssignProcessToJobObject(job, windows.Handle(handle))
	}); err != nil {
		closeJob()
		return nil, err
	}
	if assignErr != nil {
		closeJob()
		return nil, assignErr
	}
	return closeJob, nil
}
