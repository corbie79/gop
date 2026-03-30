package golang

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// CreateDesktopShortcut creates a desktop shortcut for the installed binary.
// Windows: .lnk file via PowerShell
// Linux: .desktop file
// macOS: alias in ~/Desktop
func CreateDesktopShortcut(binaryPath, name, description string) (string, error) {
	desktop, err := getDesktopPath()
	if err != nil {
		return "", fmt.Errorf("could not find desktop path: %w", err)
	}

	if err := os.MkdirAll(desktop, 0755); err != nil {
		return "", fmt.Errorf("could not create desktop directory: %w", err)
	}

	switch runtime.GOOS {
	case "windows":
		return createWindowsShortcut(binaryPath, name, description, desktop)
	case "linux":
		return createLinuxDesktopEntry(binaryPath, name, description, desktop)
	case "darwin":
		return createMacOSShortcut(binaryPath, name, description, desktop)
	default:
		return "", fmt.Errorf("desktop shortcuts not supported on %s", runtime.GOOS)
	}
}

func getDesktopPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	switch runtime.GOOS {
	case "windows":
		// Try known Windows desktop paths
		desktop := filepath.Join(home, "Desktop")
		if _, err := os.Stat(desktop); err == nil {
			return desktop, nil
		}
		// OneDrive Desktop
		onedrive := filepath.Join(home, "OneDrive", "Desktop")
		if _, err := os.Stat(onedrive); err == nil {
			return onedrive, nil
		}
		// Fallback: query via PowerShell
		cmd := exec.Command("powershell", "-Command",
			"[Environment]::GetFolderPath('Desktop')")
		out, err := cmd.Output()
		if err == nil {
			p := strings.TrimSpace(string(out))
			if p != "" {
				return p, nil
			}
		}
		return desktop, nil // default even if doesn't exist yet
	default:
		return filepath.Join(home, "Desktop"), nil
	}
}

// ===================== Windows (.lnk via PowerShell) =====================

func createWindowsShortcut(binaryPath, name, description, desktop string) (string, error) {
	shortcutPath := filepath.Join(desktop, name+".lnk")

	// Use PowerShell to create a proper .lnk shortcut
	psScript := fmt.Sprintf(`
$ws = New-Object -ComObject WScript.Shell
$sc = $ws.CreateShortcut('%s')
$sc.TargetPath = '%s'
$sc.WorkingDirectory = '%s'
$sc.Description = '%s'
$sc.Save()
`,
		shortcutPath,
		binaryPath,
		filepath.Dir(binaryPath),
		escapePS(description),
	)

	cmd := exec.Command("powershell", "-Command", psScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("PowerShell shortcut creation failed: %s: %w", string(out), err)
	}

	return shortcutPath, nil
}

func escapePS(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	return s
}

// ===================== Linux (.desktop file) =====================

func createLinuxDesktopEntry(binaryPath, name, description, desktop string) (string, error) {
	desktopFile := filepath.Join(desktop, name+".desktop")

	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Comment=%s
Exec=%s
Terminal=true
Categories=Development;
`, name, description, binaryPath)

	if err := os.WriteFile(desktopFile, []byte(content), 0755); err != nil {
		return "", fmt.Errorf("failed to write .desktop file: %w", err)
	}

	// Also install to ~/.local/share/applications for app launcher
	localApps := filepath.Join(os.Getenv("HOME"), ".local", "share", "applications")
	os.MkdirAll(localApps, 0755)
	localFile := filepath.Join(localApps, name+".desktop")
	os.WriteFile(localFile, []byte(content), 0755)

	return desktopFile, nil
}

// ===================== macOS (symlink on Desktop) =====================

func createMacOSShortcut(binaryPath, name, description, desktop string) (string, error) {
	// For CLI tools: create a symlink on Desktop
	linkPath := filepath.Join(desktop, name)

	// Remove existing link
	os.Remove(linkPath)

	if err := os.Symlink(binaryPath, linkPath); err != nil {
		return "", fmt.Errorf("failed to create symlink: %w", err)
	}

	return linkPath, nil
}

// RemoveDesktopShortcut removes the desktop shortcut for a package.
func RemoveDesktopShortcut(name string) {
	desktop, err := getDesktopPath()
	if err != nil {
		return
	}

	switch runtime.GOOS {
	case "windows":
		os.Remove(filepath.Join(desktop, name+".lnk"))
	case "linux":
		os.Remove(filepath.Join(desktop, name+".desktop"))
		// Also remove from app launcher
		home, _ := os.UserHomeDir()
		os.Remove(filepath.Join(home, ".local", "share", "applications", name+".desktop"))
	case "darwin":
		os.Remove(filepath.Join(desktop, name))
	}
}
