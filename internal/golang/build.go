package golang

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BuildAndInstall builds a Go package from srcDir and copies the binary to ~/.gop/bin.
// Returns the path to the installed binary.
func BuildAndInstall(goBin, srcDir, name string) (string, error) {
	binDir, err := EnsureBinDir()
	if err != nil {
		return "", err
	}

	binaryName := name
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	outputPath := filepath.Join(binDir, binaryName)

	fmt.Printf("Building %s for %s/%s...\n", name, runtime.GOOS, runtime.GOARCH)

	// Check if the directory contains a go.mod (is a Go module)
	goModPath := filepath.Join(srcDir, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		return "", fmt.Errorf("no go.mod found in %s - not a Go module", srcDir)
	}

	// Find build target: look for main package
	buildTarget := findBuildTarget(srcDir)

	// Run go build
	args := []string{"build", "-o", outputPath}
	args = append(args, buildTarget)

	cmd := exec.Command(goBin, args...)
	cmd.Dir = srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = buildEnv(goBin)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}

	fmt.Printf("Built %s -> %s\n", name, outputPath)
	return outputPath, nil
}

// findBuildTarget determines what to build in the source directory.
func findBuildTarget(srcDir string) string {
	// Check root for main.go
	mainGo := filepath.Join(srcDir, "main.go")
	if _, err := os.Stat(mainGo); err == nil {
		return "."
	}

	// Check cmd/<name>/main.go pattern
	cmdDir := filepath.Join(srcDir, "cmd")
	if entries, err := os.ReadDir(cmdDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				mg := filepath.Join(cmdDir, e.Name(), "main.go")
				if _, err := os.Stat(mg); err == nil {
					return "./cmd/" + e.Name()
				}
			}
		}
		// Single cmd directory
		if len(entries) == 1 && entries[0].IsDir() {
			return "./cmd/" + entries[0].Name()
		}
	}

	// Default to root
	return "."
}

// buildEnv returns environment variables for go build.
func buildEnv(goBin string) []string {
	env := os.Environ()

	goRoot := filepath.Dir(filepath.Dir(goBin))
	hasGoRoot := false
	hasGoPath := false

	for _, e := range env {
		if strings.HasPrefix(e, "GOROOT=") {
			hasGoRoot = true
		}
		if strings.HasPrefix(e, "GOPATH=") {
			hasGoPath = true
		}
	}

	// Set GOROOT if using our installed Go
	gopHome := GopHome()
	if strings.HasPrefix(goBin, gopHome) && !hasGoRoot {
		env = append(env, "GOROOT="+goRoot)
	}

	if !hasGoPath {
		goPath := filepath.Join(gopHome, "gopath")
		os.MkdirAll(goPath, 0755)
		env = append(env, "GOPATH="+goPath)
	}

	// Set GOOS/GOARCH to current platform
	env = append(env, "GOOS="+runtime.GOOS)
	env = append(env, "GOARCH="+runtime.GOARCH)

	return env
}
