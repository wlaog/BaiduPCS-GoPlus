package pcscommand

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/qjfoidnh/BaiduPCS-Go/internal/pcsconfig"
	"golang.org/x/sys/windows"
)

// A separate Edge profile avoids handing this URL to a personal browser
// running at a different elevation, which can prompt to restart that browser.
func openBrowser(url string) error {
	var edge string
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA")} {
		if base == "" {
			continue
		}
		candidate := filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			edge = candidate
			break
		}
	}
	if edge == "" {
		edge, _ = exec.LookPath("msedge.exe")
	}
	if edge == "" {
		return fmt.Errorf("未找到 Edge，请在浏览器中手动打开界面地址")
	}
	configDir := pcsconfig.GetConfigDir()
	elevated := windows.GetCurrentProcessToken().IsElevated()
	if err := os.MkdirAll(edgeBrowserProfile(configDir, elevated), 0700); err != nil {
		return fmt.Errorf("创建界面浏览器配置目录失败: %w", err)
	}
	cmd := edgeBrowserCommand(edge, configDir, url, elevated)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func edgeBrowserCommand(edge, configDir, url string, elevated bool) *exec.Cmd {
	return exec.Command(edge,
		"--user-data-dir="+edgeBrowserProfile(configDir, elevated),
		"--no-first-run", "--no-default-browser-check", "--app="+url)
}

func edgeBrowserProfile(configDir string, elevated bool) string {
	profile := "gui-browser"
	if elevated {
		profile += "-elevated"
	}
	return filepath.Join(configDir, profile)
}
