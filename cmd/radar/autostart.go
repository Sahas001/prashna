package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// autostart makes Radar start in the background when you log in, so the
// edition is ready and `radar` (or a bookmark) opens instantly.
func (a *app) autostart(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "status") {
		return errors.New("usage: radar autostart on|off|status")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	runArgs := []string{exe, "-data", a.dataDir, "run"}
	f, err := autostartFile()
	if err != nil {
		return err
	}

	switch args[0] {
	case "status":
		if _, err := os.Stat(f); err == nil {
			fmt.Fprintf(a.out, "Autostart is on (%s).\n", f)
		} else {
			fmt.Fprintln(a.out, "Autostart is off.")
		}
		return nil
	case "off":
		if runtime.GOOS == "linux" && strings.HasSuffix(f, ".service") {
			exec.Command("systemctl", "--user", "disable", "--now", "radar.service").Run()
		}
		if runtime.GOOS == "darwin" {
			exec.Command("launchctl", "unload", f).Run()
		}
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fmt.Fprintln(a.out, "Autostart is off.")
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	var content string
	switch {
	case runtime.GOOS == "darwin":
		var argXML strings.Builder
		for _, s := range runArgs {
			argXML.WriteString("\n    <string>" + xmlEscape(s) + "</string>")
		}
		content = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>app.radar</string>
  <key>ProgramArguments</key>
  <array>` + argXML.String() + `
  </array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
	case runtime.GOOS == "windows":
		// A .vbs in the Startup folder runs Radar without a console window.
		quoted := make([]string, len(runArgs))
		for i, s := range runArgs {
			quoted[i] = `""` + s + `""`
		}
		content = `CreateObject("WScript.Shell").Run "` + strings.Join(quoted, " ") + `", 0, False` + "\r\n"
	case strings.HasSuffix(f, ".service"):
		content = "[Unit]\nDescription=Radar reading app\nAfter=network-online.target\n\n[Service]\nExecStart=" +
			shellJoin(runArgs) + "\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n"
	default:
		content = "[Desktop Entry]\nType=Application\nName=Radar\nExec=" + shellJoin(runArgs) + "\nX-GNOME-Autostart-enabled=true\nNoDisplay=true\n"
	}
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		return err
	}
	switch {
	case runtime.GOOS == "darwin":
		exec.Command("launchctl", "load", "-w", f).Run()
	case strings.HasSuffix(f, ".service"):
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "radar.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl: %v: %s", err, out)
		}
	}
	fmt.Fprintf(a.out, "Autostart is on: Radar will start when you log in (%s).\n", f)
	return nil
}

// autostartFile is where this OS looks for login items.
func autostartFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", "app.radar.plist"), nil
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "Radar.vbs"), nil
	default:
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		if _, err := exec.LookPath("systemctl"); err == nil {
			return filepath.Join(cfg, "systemd", "user", "radar.service"), nil
		}
		return filepath.Join(cfg, "autostart", "radar.desktop"), nil
	}
}

func shellJoin(args []string) string {
	out := make([]string, len(args))
	for i, s := range args {
		if strings.ContainsAny(s, " \t\"'\\$") {
			s = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s) + `"`
		}
		out[i] = s
	}
	return strings.Join(out, " ")
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
