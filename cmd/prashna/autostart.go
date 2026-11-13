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

// autostart makes Prashna start in the background when you log in, so the
// edition is ready and `prashna` (or a bookmark) opens instantly.
func (a *app) autostart(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "status") {
		return errors.New("usage: prashna autostart on|off|status")
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

	if args[0] != "status" {
		removeLegacyAutostart(f)
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
			exec.Command("systemctl", "--user", "disable", "--now", "prashna.service").Run()
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
  <key>Label</key><string>app.prashna</string>
  <key>ProgramArguments</key>
  <array>` + argXML.String() + `
  </array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`
	case runtime.GOOS == "windows":
		// A .vbs in the Startup folder runs Prashna without a console window.
		quoted := make([]string, len(runArgs))
		for i, s := range runArgs {
			quoted[i] = `""` + s + `""`
		}
		content = `CreateObject("WScript.Shell").Run "` + strings.Join(quoted, " ") + `", 0, False` + "\r\n"
	case strings.HasSuffix(f, ".service"):
		content = "[Unit]\nDescription=Prashna reading app\nAfter=network-online.target\n\n[Service]\nExecStart=" +
			shellJoin(runArgs) + "\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n"
	default:
		content = "[Desktop Entry]\nType=Application\nName=Prashna\nExec=" + shellJoin(runArgs) + "\nX-GNOME-Autostart-enabled=true\nNoDisplay=true\n"
	}
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		return err
	}
	switch {
	case runtime.GOOS == "darwin":
		exec.Command("launchctl", "load", "-w", f).Run()
	case strings.HasSuffix(f, ".service"):
		if out, err := exec.Command("systemctl", "--user", "enable", "--now", "prashna.service").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl: %v: %s", err, out)
		}
	}
	fmt.Fprintf(a.out, "Autostart is on: Prashna will start when you log in (%s).\n", f)
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
		return filepath.Join(home, "Library", "LaunchAgents", "app.prashna.plist"), nil
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "Prashna.vbs"), nil
	default:
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		if _, err := exec.LookPath("systemctl"); err == nil {
			return filepath.Join(cfg, "systemd", "user", "prashna.service"), nil
		}
		return filepath.Join(cfg, "autostart", "prashna.desktop"), nil
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

// removeLegacyAutostart removes the login item of the app's old name
// (Radar), which would otherwise start the old program on an empty data
// folder next to this one. f is this app's own login item; the old one
// sits beside it.
func removeLegacyAutostart(f string) {
	dir := filepath.Dir(f)
	for _, name := range []string{"radar.service", "radar.desktop", "app.radar.plist", "Radar.vbs"} {
		old := filepath.Join(dir, name)
		if _, err := os.Stat(old); err != nil {
			continue
		}
		switch name {
		case "radar.service":
			exec.Command("systemctl", "--user", "disable", "--now", name).Run()
		case "app.radar.plist":
			exec.Command("launchctl", "unload", old).Run()
		}
		os.Remove(old)
	}
}
