package golang

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	GoDownloadURL = "https://go.dev/dl"
	GoVersion     = "1.22.5" // default version to install
)

// EnsureGo checks if Go is available. If not, installs it to ~/.gop/go.
// Returns the path to the go binary.
func EnsureGo() (string, error) {
	// 1. Check if go is already in PATH
	if goPath, err := exec.LookPath("go"); err == nil {
		return goPath, nil
	}

	// 2. Check if we previously installed go at ~/.gop/go
	gopHome := GopHome()
	localGo := filepath.Join(gopHome, "go", "bin", "go")
	if runtime.GOOS == "windows" {
		localGo += ".exe"
	}
	if _, err := os.Stat(localGo); err == nil {
		return localGo, nil
	}

	// 3. Go not found - install it
	fmt.Println("Go is not installed. Installing Go automatically...")
	fmt.Printf("Version: %s | OS: %s | Arch: %s\n\n", GoVersion, runtime.GOOS, runtime.GOARCH)

	if err := installGo(gopHome); err != nil {
		return "", fmt.Errorf("failed to install Go: %w", err)
	}

	if _, err := os.Stat(localGo); err != nil {
		return "", fmt.Errorf("Go installed but binary not found at %s", localGo)
	}

	fmt.Printf("Go %s installed successfully at %s\n\n", GoVersion, filepath.Join(gopHome, "go"))
	return localGo, nil
}

func installGo(gopHome string) error {
	archiveURL := buildDownloadURL()
	fmt.Printf("Downloading %s...\n", archiveURL)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(archiveURL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	destDir := gopHome
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", destDir, err)
	}

	if runtime.GOOS == "windows" {
		return extractZip(resp.Body, destDir)
	}
	return extractTarGz(resp.Body, destDir)
}

func buildDownloadURL() string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s/go%s.%s-%s.%s", GoDownloadURL, GoVersion, runtime.GOOS, runtime.GOARCH, ext)
}

func extractTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip error: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar error: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// Validate path to prevent traversal
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractZip(r io.Reader, destDir string) error {
	// For Windows zip: download to temp file, then extract with PowerShell
	tmpFile, err := os.CreateTemp("", "go-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, r); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	cmd := exec.Command("powershell", "-Command",
		fmt.Sprintf("Expand-Archive -Path '%s' -DestinationPath '%s' -Force", tmpFile.Name(), destDir))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// GopHome returns ~/.gop path.
func GopHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".gop")
}

// BinDir returns ~/.gop/bin path.
func BinDir() string {
	return filepath.Join(GopHome(), "bin")
}
