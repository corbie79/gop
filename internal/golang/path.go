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
// Returns true if a shell profile was modified (i.e., first time only).
func EnsurePath() (bool, error) {
	binDir := BinDir()

	// Add to current process PATH if not present
	if !isInCurrentPath(binDir) {
		pathSep := string(os.PathListSeparator)
		os.Setenv("PATH", binDir+pathSep+os.Getenv("PATH"))
	}

	// Persist to shell profile (idempotent - checks file content before writing)
	switch runtime.GOOS {
	case "windows":
		return addPathWindows(binDir)
	default:
		return addPathUnix(binDir)
	}
}

func isInCurrentPath(binDir string) bool {
	pathSep := string(os.PathListSeparator)
	for _, p := range strings.Split(os.Getenv("PATH"), pathSep) {
		if filepath.Clean(p) == filepath.Clean(binDir) {
			return true
		}
	}
	return false
}

// profileContainsPath checks if a shell profile file already has the binDir in any PATH export.
func profileContainsPath(profilePath, binDir string) bool {
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return false
	}
	content := string(data)

	// Check for exact binDir path in any form:
	//   export PATH="~/.gop/bin:$PATH"
	//   export PATH="/home/user/.gop/bin:$PATH"
	//   set -gx PATH ~/.gop/bin $PATH  (fish)
	cleanBin := filepath.Clean(binDir)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		// Skip comments
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, cleanBin) && (strings.Contains(line, "PATH") || strings.Contains(line, "path")) {
			return true
		}
	}
	return false
}

func addPathWindows(binDir string) (bool, error) {
	// Get existing user PATH via PowerShell
	cmd := exec.Command("powershell", "-Command",
		"[Environment]::GetEnvironmentVariable('PATH', 'User')")
	out, err := cmd.Output()
	if err != nil {
		// Can't read user PATH; try setx only if not already set
		return false, exec.Command("setx", "PATH", fmt.Sprintf("%s;%%PATH%%", binDir)).Run()
	}

	currentPath := strings.TrimSpace(string(out))

	// Check if already in user-level PATH (exact match per segment)
	for _, p := range strings.Split(currentPath, ";") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if filepath.Clean(strings.TrimSpace(p)) == filepath.Clean(binDir) {
			return false, nil // already present, do nothing
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
		if err := cmd.Run(); err != nil {
			return false, err
		}
	} else {
		if err := exec.Command("setx", "PATH", newPath).Run(); err != nil {
			return false, err
		}
	}

	fmt.Printf("Added %s to user PATH (Windows)\n", binDir)
	return true, nil
}

func addPathUnix(binDir string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}

	exportLine := fmt.Sprintf("\n# gop package manager\nexport PATH=\"%s:$PATH\"\n", binDir)

	// Determine target shell profile
	profile := detectShellProfile(home)
	if profile == "" {
		return false, fmt.Errorf("could not detect shell profile")
	}

	// Check if already present in the file - prevent duplicates
	if profileContainsPath(profile, binDir) {
		return false, nil // already configured, do nothing
	}

	// Fish shell uses different syntax
	if strings.Contains(profile, "fish") {
		exportLine = fmt.Sprintf("\n# gop package manager\nset -gx PATH \"%s\" $PATH\n", binDir)
	}

	f, err := os.OpenFile(profile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, fmt.Errorf("could not open %s: %w", profile, err)
	}
	defer f.Close()

	if _, err := f.WriteString(exportLine); err != nil {
		return false, fmt.Errorf("could not write to %s: %w", profile, err)
	}

	fmt.Printf("Added %s to PATH in %s\n", binDir, profile)
	return true, nil
}

// detectShellProfile returns the single best profile file to modify.
// Only one file is modified to avoid duplicates across multiple profiles.
func detectShellProfile(home string) string {
	shell := os.Getenv("SHELL")

	// macOS defaults to zsh since Catalina (10.15)
	if runtime.GOOS == "darwin" && shell == "" {
		shell = "/bin/zsh"
	}

	switch {
	case strings.Contains(shell, "zsh"):
		// macOS + zsh: .zshrc is the standard interactive shell config
		return filepath.Join(home, ".zshrc")
	case strings.Contains(shell, "fish"):
		return filepath.Join(home, ".config", "fish", "config.fish")
	case strings.Contains(shell, "bash"):
		if runtime.GOOS == "darwin" {
			// macOS bash reads .bash_profile for login shells (Terminal.app opens login shell)
			return filepath.Join(home, ".bash_profile")
		}
		// Linux bash: .bashrc for interactive shells
		bashrc := filepath.Join(home, ".bashrc")
		if _, err := os.Stat(bashrc); err == nil {
			return bashrc
		}
		return filepath.Join(home, ".bash_profile")
	}

	// Unknown shell: pick first existing common profile
	if runtime.GOOS == "darwin" {
		// macOS: prefer .zshrc since it's the default shell
		for _, name := range []string{".zshrc", ".bash_profile", ".profile"} {
			p := filepath.Join(home, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return filepath.Join(home, ".zshrc")
	}

	for _, name := range []string{".bashrc", ".zshrc", ".profile"} {
		p := filepath.Join(home, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Last fallback
	return filepath.Join(home, ".profile")
}

// PrintPathInstructions shows instructions to apply PATH changes in current session.
func PrintPathInstructions() {
	binDir := BinDir()
	switch runtime.GOOS {
	case "windows":
		fmt.Println("\nPATH has been updated. Restart your terminal to apply.")
	default:
		shell := os.Getenv("SHELL")
		if runtime.GOOS == "darwin" && shell == "" {
			shell = "/bin/zsh"
		}
		switch {
		case strings.Contains(shell, "zsh"):
			fmt.Printf("\nRun: source ~/.zshrc\n")
		case strings.Contains(shell, "bash"):
			if runtime.GOOS == "darwin" {
				fmt.Printf("\nRun: source ~/.bash_profile\n")
			} else {
				fmt.Printf("\nRun: source ~/.bashrc\n")
			}
		case strings.Contains(shell, "fish"):
			fmt.Printf("\nRun: source ~/.config/fish/config.fish\n")
		default:
			fmt.Printf("\nRun: export PATH=\"%s:$PATH\"\n", binDir)
		}
		fmt.Println("Or restart your terminal to apply PATH changes.")
	}
}
