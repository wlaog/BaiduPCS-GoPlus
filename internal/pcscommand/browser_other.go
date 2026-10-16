//go:build !windows

package pcscommand

import (
	"os/exec"
	"runtime"
)

func openBrowser(url string) error {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	cmd := exec.Command(program, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
