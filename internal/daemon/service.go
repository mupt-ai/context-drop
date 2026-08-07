package daemon

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"contextdrop.dev/context-drop/internal/runtimeclient"
)

const ServiceLabel = "dev.contextdrop.daemon"
const WatchdogLabel = "dev.contextdrop.daemon.watchdog"

type ManagedServiceStatus struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"`
	Path      string `json:"path"`
	Detail    string `json:"detail,omitempty"`
}

func ServiceStatus() ManagedServiceStatus {
	path, _ := servicePath(false)
	out := ManagedServiceStatus{Supported: runtime.GOOS == "darwin" || runtime.GOOS == "linux", Path: path}
	_, err := os.Stat(path)
	out.Installed = err == nil
	if runtime.GOOS == "darwin" {
		out.Loaded = commandOK("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), ServiceLabel))
	}
	if runtime.GOOS == "linux" {
		out.Loaded = commandOK("systemctl", "--user", "is-active", "--quiet", "context-drop-daemon.service")
	}
	if !out.Supported {
		out.Detail = "background service is supported on macOS launchd and Linux systemd --user"
	}
	return out
}

func WatchdogStatus() ManagedServiceStatus {
	if runtime.GOOS == "linux" {
		return ManagedServiceStatus{Supported: false, Detail: "Linux uses Restart=on-failure in the daemon systemd user service; no separate watchdog is needed"}
	}
	path, _ := servicePath(true)
	out := ManagedServiceStatus{Supported: runtime.GOOS == "darwin", Path: path}
	_, err := os.Stat(path)
	out.Installed = err == nil
	if runtime.GOOS == "darwin" {
		out.Loaded = commandOK("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), WatchdogLabel))
	}
	return out
}

func servicePath(watchdog bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		label := ServiceLabel
		if watchdog {
			label = WatchdogLabel
		}
		return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
	}
	if runtime.GOOS == "linux" {
		return filepath.Join(home, ".config", "systemd", "user", "context-drop-daemon.service"), nil
	}
	return "", fmt.Errorf("background service is unsupported on %s", runtime.GOOS)
}

func RenderLaunchAgent(exe, logPath, nodePath string, watchdog bool) string {
	label := ServiceLabel
	// Messages.app access is granted by macOS TCC to the responsible launch
	// executable. Run the daemon beneath the configured Node binary because it
	// is the same stable, user-approved executable used for local integrations;
	// the wrapper only forwards signals and the child exit status.
	wrapper := `const {spawn}=require("node:child_process");const child=spawn(process.argv[1],["daemon","run"],{stdio:"inherit"});for(const signal of ["SIGTERM","SIGINT"])process.on(signal,()=>child.kill(signal));child.on("exit",(code,signal)=>process.exit(code??(signal?1:0)));`
	args := []string{nodePath, "-e", wrapper, exe}
	interval := ""
	keepAlive := "<key>KeepAlive</key><true/>\n"
	throttle := ""
	if watchdog {
		label = WatchdogLabel
		args = []string{exe, "daemon", "watchdog", "check"}
		interval = "<key>StartInterval</key><integer>900</integer>\n"
		keepAlive = ""
	} else {
		throttle = "<key>ThrottleInterval</key><integer>30</integer>\n"
	}
	argXML := ""
	for _, arg := range args {
		argXML += "<string>" + xmlEscape(arg) + "</string>"
	}
	servicePath := strings.Join([]string{filepath.Dir(nodePath), filepath.Dir(exe), "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"}, ":")
	home, _ := os.UserHomeDir()
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array>%s</array>
<key>RunAtLoad</key><true/>
<key>ProcessType</key><string>Background</string>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string><key>HOME</key><string>%s</string></dict>
%s%s%s<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, label, argXML, xmlEscape(servicePath), xmlEscape(home), keepAlive, interval, throttle, xmlEscape(logPath), xmlEscape(logPath))
}

func RenderSystemd(exe, nodePath string) string {
	return fmt.Sprintf(`[Unit]
Description=Context Drop local orchestration daemon
After=network-online.target

[Service]
Type=simple
Environment="PATH=%s:/usr/local/bin:/usr/bin:/bin"
ExecStart=%s daemon run
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, filepath.Dir(nodePath), systemdEscape(exe))
}

func StartBackground() (PIDInfo, error) {
	status := ServiceStatus()
	if !status.Installed {
		return Start()
	}
	if runtime.GOOS == "darwin" {
		target := fmt.Sprintf("gui/%d", os.Getuid())
		if !status.Loaded {
			output, err := exec.Command("launchctl", "bootstrap", target, status.Path).CombinedOutput()
			if err != nil {
				return PIDInfo{}, fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(output)))
			}
		}
	} else if runtime.GOOS == "linux" {
		output, err := exec.Command("systemctl", "--user", "start", "context-drop-daemon.service").CombinedOutput()
		if err != nil {
			return PIDInfo{}, fmt.Errorf("systemctl start: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	return waitForPID(5 * time.Second)
}

func StopBackground() error {
	status := ServiceStatus()
	if status.Loaded {
		if runtime.GOOS == "darwin" {
			_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), ServiceLabel)).Run()
		} else if runtime.GOOS == "linux" {
			_ = exec.Command("systemctl", "--user", "stop", "context-drop-daemon.service").Run()
		}
	}
	return Stop()
}

func InstallService(watchdog bool) error {
	if watchdog && runtime.GOOS == "linux" {
		return fmt.Errorf("Linux uses Restart=on-failure; no separate watchdog is needed")
	}
	path, err := servicePath(watchdog)
	if err != nil {
		return err
	}
	exe, err := canonicalExecutable()
	if err != nil {
		return err
	}
	if _, err := runtimeclient.Initialize(); err != nil {
		return err
	}
	runtimeConfig, err := runtimeclient.LoadConfig()
	if err != nil {
		return err
	}
	_, _, logPath, err := Paths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	if !watchdog {
		// A service-owned daemon must be the only process holding process.lock.
		// Stop an unmanaged instance before installing/loading the service.
		if err := StopBackground(); err != nil {
			return fmt.Errorf("stop existing Context Drop daemon before service install: %w", err)
		}
	}
	content := RenderSystemd(exe, runtimeConfig.NodePath)
	if runtime.GOOS == "darwin" {
		content = RenderLaunchAgent(exe, logPath, runtimeConfig.NodePath, watchdog)
	}
	oldContent, oldErr := os.ReadFile(path)
	if oldErr != nil && !os.IsNotExist(oldErr) {
		return oldErr
	}
	if err := writeServiceFile(path, []byte(content)); err != nil {
		return err
	}
	rollback := func() {
		if oldErr == nil {
			_ = writeServiceFile(path, oldContent)
		} else {
			_ = os.Remove(path)
		}
	}
	if runtime.GOOS == "darwin" {
		target := fmt.Sprintf("gui/%d", os.Getuid())
		label := ServiceLabel
		if watchdog {
			label = WatchdogLabel
		}
		_ = exec.Command("launchctl", "bootout", target+"/"+label).Run()
		output, e := exec.Command("launchctl", "bootstrap", target, path).CombinedOutput()
		if e != nil {
			rollback()
			return fmt.Errorf("launchctl bootstrap: %w: %s", e, strings.TrimSpace(string(output)))
		}
		return nil
	}
	output, e := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	if e != nil {
		rollback()
		return fmt.Errorf("systemctl daemon-reload: %w: %s", e, strings.TrimSpace(string(output)))
	}
	output, e = exec.Command("systemctl", "--user", "enable", "--now", "context-drop-daemon.service").CombinedOutput()
	if e != nil {
		rollback()
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return fmt.Errorf("systemctl enable: %w: %s", e, strings.TrimSpace(string(output)))
	}
	return nil
}

func UninstallService(watchdog bool) error {
	if watchdog && runtime.GOOS == "linux" {
		return fmt.Errorf("Linux has no separate Context Drop watchdog")
	}
	path, err := servicePath(watchdog)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		label := ServiceLabel
		if watchdog {
			label = WatchdogLabel
		}
		_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).Run()
		if !watchdog {
			_ = Stop()
		}
	} else {
		_ = exec.Command("systemctl", "--user", "disable", "--now", "context-drop-daemon.service").Run()
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func WatchdogCheck() error {
	status := ServiceStatus()
	if !status.Installed {
		return nil
	}
	if status.Loaded {
		return nil
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	output, err := exec.Command("launchctl", "bootstrap", target, status.Path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("watchdog restart failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func writeServiceFile(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".context-drop-service-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(content)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func commandOK(name string, args ...string) bool { return exec.Command(name, args...).Run() == nil }
func xmlEscape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
func systemdEscape(value string) string { return strings.ReplaceAll(value, " ", `\x20`) }
