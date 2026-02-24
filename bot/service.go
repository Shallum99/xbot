package bot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// ServicePaths holds platform-specific paths for the background service.
type ServicePaths struct {
	ConfigPath string // systemd unit or launchd plist
	LogPath    string
}

// GetServicePaths returns the paths for the current platform.
func GetServicePaths() (*ServicePaths, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("getting home directory: %w", err)
	}

	switch runtime.GOOS {
	case "linux":
		return &ServicePaths{
			ConfigPath: filepath.Join(homeDir, ".config", "systemd", "user", "xbot.service"),
			LogPath:    "", // journalctl handles logs
		}, nil
	case "darwin":
		return &ServicePaths{
			ConfigPath: filepath.Join(homeDir, "Library", "LaunchAgents", "com.xbot.agent.plist"),
			LogPath:    filepath.Join(homeDir, "Library", "Logs", "xbot.log"),
		}, nil
	default:
		return nil, fmt.Errorf("unsupported platform: %s (use systemd on Linux or launchd on macOS)", runtime.GOOS)
	}
}

// xbotBinaryPath returns the absolute path of the running xbot binary.
func xbotBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding xbot binary: %w", err)
	}
	return filepath.EvalSymlinks(exe)
}

// ServiceInstall generates and writes the service config for the current platform.
func ServiceInstall() error {
	binPath, err := xbotBinaryPath()
	if err != nil {
		return err
	}

	paths, err := GetServicePaths()
	if err != nil {
		return err
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath), 0755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	switch runtime.GOOS {
	case "linux":
		return installSystemd(binPath, paths)
	case "darwin":
		return installLaunchd(binPath, paths)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// ServiceUninstall stops and removes the service config.
func ServiceUninstall() error {
	// Stop first, ignore errors (might not be running)
	_ = ServiceStop()

	paths, err := GetServicePaths()
	if err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		// Unload from launchd
		_ = runCmd("launchctl", "unload", paths.ConfigPath)
	case "linux":
		// Disable from systemd
		_ = runCmd("systemctl", "--user", "disable", "xbot")
	}

	if err := os.Remove(paths.ConfigPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing service config: %w", err)
	}

	if runtime.GOOS == "linux" {
		_ = runCmd("systemctl", "--user", "daemon-reload")
	}

	return nil
}

// ServiceStart starts the background service.
func ServiceStart() error {
	paths, err := GetServicePaths()
	if err != nil {
		return err
	}

	if _, err := os.Stat(paths.ConfigPath); os.IsNotExist(err) {
		return fmt.Errorf("service not installed — run 'xbot service install' first")
	}

	switch runtime.GOOS {
	case "linux":
		return runCmd("systemctl", "--user", "start", "xbot")
	case "darwin":
		return runCmd("launchctl", "load", paths.ConfigPath)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// ServiceStop stops the background service.
func ServiceStop() error {
	switch runtime.GOOS {
	case "linux":
		return runCmd("systemctl", "--user", "stop", "xbot")
	case "darwin":
		paths, err := GetServicePaths()
		if err != nil {
			return err
		}
		return runCmd("launchctl", "unload", paths.ConfigPath)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// ServiceStatus prints the service status.
func ServiceStatus() error {
	switch runtime.GOOS {
	case "linux":
		return runCmdPassthrough("systemctl", "--user", "status", "xbot")
	case "darwin":
		return runCmdPassthrough("launchctl", "list", "com.xbot.agent")
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// ServiceLogs tails the service logs.
func ServiceLogs() error {
	switch runtime.GOOS {
	case "linux":
		return runCmdPassthrough("journalctl", "--user", "-u", "xbot", "-f", "--no-pager")
	case "darwin":
		paths, err := GetServicePaths()
		if err != nil {
			return err
		}
		if _, err := os.Stat(paths.LogPath); os.IsNotExist(err) {
			return fmt.Errorf("no logs yet — is the service running?")
		}
		return runCmdPassthrough("tail", "-f", paths.LogPath)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

// ─── systemd ─────────────────────────────────────────────────────

const systemdTemplate = `[Unit]
Description=xbot — AI bug fix bot triggered from X
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.BinPath}} start
Restart=on-failure
RestartSec=10
Environment=HOME={{.HomeDir}}
{{- range .EnvVars}}
Environment={{.}}
{{- end}}

[Install]
WantedBy=default.target
`

func installSystemd(binPath string, paths *ServicePaths) error {
	homeDir, _ := os.UserHomeDir()

	data := struct {
		BinPath string
		HomeDir string
		EnvVars []string
	}{
		BinPath: binPath,
		HomeDir: homeDir,
		EnvVars: collectEnvVars(),
	}

	var buf strings.Builder
	tmpl, err := template.New("systemd").Parse(systemdTemplate)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}

	if err := os.WriteFile(paths.ConfigPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("writing service file: %w", err)
	}

	// Reload and enable
	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("reloading systemd: %w", err)
	}
	if err := runCmd("systemctl", "--user", "enable", "xbot"); err != nil {
		return fmt.Errorf("enabling service: %w", err)
	}

	return nil
}

// ─── launchd ─────────────────────────────────────────────────────

const launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.xbot.agent</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinPath}}</string>
        <string>start</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>{{.LogPath}}</string>
    <key>StandardErrorPath</key>
    <string>{{.LogPath}}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>{{.HomeDir}}</string>
{{- range .EnvEntries}}
        <key>{{.Key}}</key>
        <string>{{.Value}}</string>
{{- end}}
    </dict>
</dict>
</plist>
`

type envEntry struct {
	Key   string
	Value string
}

func installLaunchd(binPath string, paths *ServicePaths) error {
	homeDir, _ := os.UserHomeDir()

	data := struct {
		BinPath    string
		LogPath    string
		HomeDir    string
		EnvEntries []envEntry
	}{
		BinPath:    binPath,
		LogPath:    paths.LogPath,
		HomeDir:    homeDir,
		EnvEntries: collectEnvEntries(),
	}

	var buf strings.Builder
	tmpl, err := template.New("launchd").Parse(launchdTemplate)
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering template: %w", err)
	}

	if err := os.WriteFile(paths.ConfigPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}

	return nil
}

// ─── helpers ─────────────────────────────────────────────────────

// collectEnvVars returns KEY=VALUE strings for agent API keys.
func collectEnvVars() []string {
	var vars []string
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"} {
		if val := os.Getenv(key); val != "" {
			vars = append(vars, key+"="+val)
		}
	}
	return vars
}

// collectEnvEntries returns key/value pairs for launchd plist.
func collectEnvEntries() []envEntry {
	var entries []envEntry
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"} {
		if val := os.Getenv(key); val != "" {
			entries = append(entries, envEntry{Key: key, Value: val})
		}
	}
	return entries
}

// runCmd runs a command and returns an error if it fails.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runCmdPassthrough runs a command with full I/O passthrough.
func runCmdPassthrough(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
