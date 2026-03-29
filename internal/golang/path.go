package golang

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// EnsureBinDir creates ~/.gop/bin if it doesn't exist.
func EnsureBinDir() (string, error) {
	binDir := BinDir()
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create bin directory: %w", err)
	}
	return binDir, nil
}

// EnsurePath checks if ~/.gop/bin is in PATH. If not, adds it.
// Returns true if PATH was modified.
func EnsurePath() (bool, error) {
	binDir := BinDir()

	// Check if already in PATH
	pathEnv := os.Getenv("PATH")
	pathSep := string(os.PathListSeparator)
	for _, p := range strings.Split(pathEnv, pathSep) {
		if filepath.Clean(p) == filepath.Clean(binDir) {
			return false, nil
		}
	}

	// Add to PATH for current process
	os.Setenv("PATH", binDir+pathSep+pathEnv)

	// Persist to shell profile
	switch runtime.GOOS {
	case "windows":
		return true, addPathWindows(binDir)
	default:
		return true, addPathUnix(binDir)
	}
}

func addPathWindows(binDir string) error {
	// Use setx to persist PATH for the user
	// First get existing user PATH
	cmd := exec.Command("powershell", "-Command",
		"[Environment]::GetEnvironmentVariable('PATH', 'User')")
	out, err := cmd.Output()
	if err != nil {
		// Fallback to setx directly
		return exec.Command("setx", "PATH", fmt.Sprintf("%s;%%PATH%%", binDir)).Run()
	}

	currentPath := strings.TrimSpace(string(out))

	// Check if already in user PATH
	for _, p := range strings.Split(currentPath, ";") {
		if filepath.Clean(p) == filepath.Clean(binDir) {
			return nil
		}
	}

	newPath := binDir
	if currentPath != "" {
		newPath = binDir + ";" + currentPath
	}

	// setx has a 1024 character limit, use PowerShell for longer paths
	if len(newPath) > 1024 {
		cmd := exec.Command("powershell", "-Command",
			fmt.Sprintf("[Environment]::SetEnvironmentVariable('PATH', '%s', 'User')", newPath))
		return cmd.Run()
	}

	return exec.Command("setx", "PATH", newPath).Run()
}

func addPathUnix(binDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	exportLine := fmt.Sprintf("\n# gop package manager\nexport PATH=\"%s:$PATH\"\n", binDir)

	// Determine which shell profile to modify
	profiles := detectShellProfiles(home)

	modified := false
	for _, profile := range profiles {
		// Check if already added
		data, err := os.ReadFile(profile)
		if err == nil && strings.Contains(string(data), binDir) {
			continue
		}

		f, err := os.OpenFile(profile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			continue
		}
		if _, err := f.WriteString(exportLine); err != nil {
			f.Close()
			continue
		}
		f.Close()
		modified = true
		fmt.Printf("Added %s to PATH in %s\n", binDir, profile)
	}

	if !modified {
		// Fallback: try .profile
		profile := filepath.Join(home, ".profile")
		f, err := os.OpenFile(profile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("could not update any shell profile: %w", err)
		}
		defer f.Close()
		if _, err := f.WriteString(exportLine); err != nil {
			return err
		}
		fmt.Printf("Added %s to PATH in %s\n", binDir, profile)
	}

	return nil
}

func detectShellProfiles(home string) []string {
	var profiles []string

	shell := os.Getenv("SHELL")

	switch {
	case strings.Contains(shell, "zsh"):
		profiles = append(profiles, filepath.Join(home, ".zshrc"))
	case strings.Contains(shell, "bash"):
		// Prefer .bashrc for interactive shells, also add .bash_profile
		bashrc := filepath.Join(home, ".bashrc")
		if _, err := os.Stat(bashrc); err == nil {
			profiles = append(profiles, bashrc)
		}
		bashProfile := filepath.Join(home, ".bash_profile")
		if _, err := os.Stat(bashProfile); err == nil {
			profiles = append(profiles, bashProfile)
		}
	case strings.Contains(shell, "fish"):
		fishConfig := filepath.Join(home, ".config", "fish", "config.fish")
		profiles = append(profiles, fishConfig)
	}

	// If no specific shell detected, try common ones
	if len(profiles) == 0 {
		for _, name := range []string{".bashrc", ".zshrc", ".profile"} {
			p := filepath.Join(home, name)
			if _, err := os.Stat(p); err == nil {
				profiles = append(profiles, p)
				break
			}
		}
	}

	return profiles
}

// PrintPathInstructions shows manual instructions to apply PATH changes.
func PrintPathInstructions() {
	binDir := BinDir()
	switch runtime.GOOS {
	case "windows":
		fmt.Println("\nPATH has been updated via setx. Restart your terminal to apply.")
	default:
		shell := os.Getenv("SHELL")
		switch {
		case strings.Contains(shell, "zsh"):
			fmt.Printf("\nRun: source ~/.zshrc\n")
		case strings.Contains(shell, "bash"):
			fmt.Printf("\nRun: source ~/.bashrc\n")
		case strings.Contains(shell, "fish"):
			fmt.Printf("\nRun: source ~/.config/fish/config.fish\n")
		default:
			fmt.Printf("\nRun: export PATH=\"%s:$PATH\"\n", binDir)
		}
		fmt.Println("Or restart your terminal to apply PATH changes.")
	}
}
