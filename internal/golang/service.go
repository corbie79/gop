package golang

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// ServiceConfig holds the configuration for registering a system service.
type ServiceConfig struct {
	Name        string
	DisplayName string
	Description string
	BinaryPath  string
	Args        []string
	WorkingDir  string
	User        string
	AutoRestart bool
}

// RegisterService registers the binary as a system service.
// Windows: sc.exe (Service Control Manager)
// Linux: systemd unit file
// macOS: launchd plist
func RegisterService(cfg ServiceConfig) (string, error) {
	if cfg.Name == "" || cfg.BinaryPath == "" {
		return "", fmt.Errorf("service name and binary path are required")
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = cfg.Name
	}
	if cfg.Description == "" {
		cfg.Description = fmt.Sprintf("%s service (installed by gop)", cfg.Name)
	}
	if cfg.WorkingDir == "" {
		cfg.WorkingDir = filepath.Dir(cfg.BinaryPath)
	}

	switch runtime.GOOS {
	case "windows":
		return registerWindowsService(cfg)
	case "linux":
		return registerSystemdService(cfg)
	case "darwin":
		return registerLaunchdService(cfg)
	default:
		return "", fmt.Errorf("service registration not supported on %s", runtime.GOOS)
	}
}

// UnregisterService stops and removes the system service.
func UnregisterService(name string) error {
	switch runtime.GOOS {
	case "windows":
		return unregisterWindowsService(name)
	case "linux":
		return unregisterSystemdService(name)
	case "darwin":
		return unregisterLaunchdService(name)
	default:
		return fmt.Errorf("service removal not supported on %s", runtime.GOOS)
	}
}

// IsServiceRegistered checks if a service with the given name exists.
func IsServiceRegistered(name string) bool {
	switch runtime.GOOS {
	case "windows":
		cmd := exec.Command("sc", "query", name)
		return cmd.Run() == nil
	case "linux":
		unitFile := filepath.Join("/etc/systemd/system", "gop-"+name+".service")
		userUnit := systemdUserUnitPath(name)
		_, err1 := os.Stat(unitFile)
		_, err2 := os.Stat(userUnit)
		return err1 == nil || err2 == nil
	case "darwin":
		plistPath := launchdPlistPath(name)
		_, err := os.Stat(plistPath)
		return err == nil
	}
	return false
}

// ===================== Windows (sc.exe) =====================

func registerWindowsService(cfg ServiceConfig) (string, error) {
	binPath := cfg.BinaryPath
	if len(cfg.Args) > 0 {
		binPath = fmt.Sprintf(`"%s" %s`, cfg.BinaryPath, strings.Join(cfg.Args, " "))
	}

	// Create service
	args := []string{
		"create", cfg.Name,
		"binPath=", binPath,
		"DisplayName=", cfg.DisplayName,
		"start=", "auto",
	}

	cmd := exec.Command("sc", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sc create failed: %s: %w", string(out), err)
	}

	// Set description
	exec.Command("sc", "description", cfg.Name, cfg.Description).Run()

	// Set failure recovery: restart after 5 seconds
	if cfg.AutoRestart {
		exec.Command("sc", "failure", cfg.Name, "reset=", "86400", "actions=", "restart/5000/restart/10000/restart/30000").Run()
	}

	// Start service
	startCmd := exec.Command("sc", "start", cfg.Name)
	startOut, err := startCmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Warning: service created but failed to start: %s\n", string(startOut))
	} else {
		fmt.Printf("Service %q started.\n", cfg.Name)
	}

	return fmt.Sprintf("Windows Service: %s", cfg.Name), nil
}

func unregisterWindowsService(name string) error {
	// Stop service first
	stopCmd := exec.Command("sc", "stop", name)
	stopCmd.Run() // ignore error (might not be running)

	// Delete service
	cmd := exec.Command("sc", "delete", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc delete failed: %s: %w", string(out), err)
	}

	fmt.Printf("Removed Windows service: %s\n", name)
	return nil
}

// ===================== Linux (systemd) =====================

var systemdTemplate = `[Unit]
Description={{.Description}}
After=network.target

[Service]
Type=simple
ExecStart={{.BinaryPath}}{{range .Args}} {{.}}{{end}}
WorkingDirectory={{.WorkingDir}}
{{- if .User}}
User={{.User}}
{{- end}}
Restart={{if .AutoRestart}}on-failure{{else}}no{{end}}
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`

func registerSystemdService(cfg ServiceConfig) (string, error) {
	serviceName := "gop-" + cfg.Name

	// Try user-level first (no sudo), then system-level
	unitPath, isUser, err := writeSystemdUnit(cfg, serviceName)
	if err != nil {
		return "", err
	}

	if isUser {
		// User-level systemd
		run("systemctl", "--user", "daemon-reload")
		run("systemctl", "--user", "enable", serviceName+".service")
		out, err := runOutput("systemctl", "--user", "start", serviceName+".service")
		if err != nil {
			fmt.Printf("Warning: service created but failed to start: %s\n", out)
		} else {
			fmt.Printf("Service started (user): %s\n", serviceName)
		}
	} else {
		// System-level
		run("systemctl", "daemon-reload")
		run("systemctl", "enable", serviceName+".service")
		out, err := runOutput("systemctl", "start", serviceName+".service")
		if err != nil {
			fmt.Printf("Warning: service created but failed to start: %s\n", out)
		} else {
			fmt.Printf("Service started (system): %s\n", serviceName)
		}
	}

	return unitPath, nil
}

func writeSystemdUnit(cfg ServiceConfig, serviceName string) (string, bool, error) {
	tmpl, err := template.New("systemd").Parse(systemdTemplate)
	if err != nil {
		return "", false, err
	}

	// Try user-level first (~/.config/systemd/user/)
	userDir := systemdUserDir()
	os.MkdirAll(userDir, 0755)
	userPath := filepath.Join(userDir, serviceName+".service")

	f, err := os.Create(userPath)
	if err == nil {
		defer f.Close()
		if err := tmpl.Execute(f, cfg); err != nil {
			return "", false, fmt.Errorf("failed to write unit file: %w", err)
		}
		fmt.Printf("Created systemd unit: %s\n", userPath)
		return userPath, true, nil
	}

	// Fallback: system-level (needs sudo)
	systemPath := filepath.Join("/etc/systemd/system", serviceName+".service")
	tmpFile, err := os.CreateTemp("", serviceName+"*.service")
	if err != nil {
		return "", false, err
	}
	if err := tmpl.Execute(tmpFile, cfg); err != nil {
		tmpFile.Close()
		return "", false, err
	}
	tmpFile.Close()

	// Copy with sudo
	cmd := exec.Command("sudo", "cp", tmpFile.Name(), systemPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmpFile.Name())
		return "", false, fmt.Errorf("failed to install system service (sudo required): %w", err)
	}
	os.Remove(tmpFile.Name())
	fmt.Printf("Created systemd unit: %s\n", systemPath)
	return systemPath, false, nil
}

func unregisterSystemdService(name string) error {
	serviceName := "gop-" + name

	// Try user-level first
	userUnit := systemdUserUnitPath(name)
	if _, err := os.Stat(userUnit); err == nil {
		run("systemctl", "--user", "stop", serviceName+".service")
		run("systemctl", "--user", "disable", serviceName+".service")
		os.Remove(userUnit)
		run("systemctl", "--user", "daemon-reload")
		fmt.Printf("Removed systemd user service: %s\n", serviceName)
		return nil
	}

	// System-level
	systemUnit := filepath.Join("/etc/systemd/system", serviceName+".service")
	if _, err := os.Stat(systemUnit); err == nil {
		run("sudo", "systemctl", "stop", serviceName+".service")
		run("sudo", "systemctl", "disable", serviceName+".service")
		exec.Command("sudo", "rm", systemUnit).Run()
		run("sudo", "systemctl", "daemon-reload")
		fmt.Printf("Removed systemd system service: %s\n", serviceName)
		return nil
	}

	return nil // not found = already removed
}

func systemdUserDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

func systemdUserUnitPath(name string) string {
	return filepath.Join(systemdUserDir(), "gop-"+name+".service")
}

// ===================== macOS (launchd) =====================

var launchdTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.gop.{{.Name}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
{{- range .Args}}
        <string>{{.}}</string>
{{- end}}
    </array>
    <key>WorkingDirectory</key>
    <string>{{.WorkingDir}}</string>
    <key>RunAtLoad</key>
    <true/>
{{- if .AutoRestart}}
    <key>KeepAlive</key>
    <true/>
{{- end}}
    <key>StandardOutPath</key>
    <string>/tmp/gop-{{.Name}}.stdout.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/gop-{{.Name}}.stderr.log</string>
</dict>
</plist>
`

func registerLaunchdService(cfg ServiceConfig) (string, error) {
	plistPath := launchdPlistPath(cfg.Name)

	if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
		return "", err
	}

	tmpl, err := template.New("launchd").Parse(launchdTemplate)
	if err != nil {
		return "", err
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return "", fmt.Errorf("failed to create plist: %w", err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, cfg); err != nil {
		return "", fmt.Errorf("failed to write plist: %w", err)
	}

	fmt.Printf("Created launchd plist: %s\n", plistPath)

	// Load service
	out, err := runOutput("launchctl", "load", plistPath)
	if err != nil {
		fmt.Printf("Warning: failed to load service: %s\n", out)
	} else {
		fmt.Printf("Service loaded: com.gop.%s\n", cfg.Name)
	}

	return plistPath, nil
}

func unregisterLaunchdService(name string) error {
	plistPath := launchdPlistPath(name)

	if _, err := os.Stat(plistPath); os.IsNotExist(err) {
		return nil
	}

	// Unload first
	exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)

	fmt.Printf("Removed launchd service: com.gop.%s\n", name)
	return nil
}

func launchdPlistPath(name string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", "com.gop."+name+".plist")
}

// ===================== Helpers =====================

func run(name string, args ...string) {
	exec.Command(name, args...).Run()
}

func runOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
