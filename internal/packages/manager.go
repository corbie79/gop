package packages

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/corbie79/gop/internal/config"
	"github.com/corbie79/gop/internal/gitclient"
	"github.com/corbie79/gop/internal/golang"
	"github.com/corbie79/gop/internal/lockfile"
)

type Manager struct {
	Config     *config.Config
	LockFile   *lockfile.LockFile
	ConfigPath string
	LockPath   string
	BaseDir    string
}

type PackageInfo struct {
	Name           string
	Source         string
	Version        string
	ResolvedCommit string
	InstalledAt    time.Time
	Installed      bool
	BinaryPath     string
}

func NewManager(cfg *config.Config, lf *lockfile.LockFile, configPath, lockPath, baseDir string) *Manager {
	return &Manager{
		Config:     cfg,
		LockFile:   lf,
		ConfigPath: configPath,
		LockPath:   lockPath,
		BaseDir:    baseDir,
	}
}

func (m *Manager) Install(nameOrURL, version, name string) error {
	// Resolve source URL and token
	gitURL, token, err := m.Config.ResolveSource(nameOrURL)
	if err != nil {
		return err
	}

	// Determine package name
	if name == "" {
		name = gitclient.ExtractRepoName(gitURL)
	}

	// Check if already installed
	installDir := filepath.Join(m.BaseDir, m.Config.InstallDir, name)
	if _, err := os.Stat(installDir); err == nil {
		return fmt.Errorf("package %q already installed at %s (use 'gop update' to update)", name, installDir)
	}

	// Clone
	gc := gitclient.New(token)
	fmt.Printf("Installing %s from %s...\n", name, gitURL)
	result, err := gc.Clone(gitURL, installDir, version)
	if err != nil {
		os.RemoveAll(installDir)
		return err
	}

	// Check if this is a Go project and build it
	binaryPath := ""
	goModPath := filepath.Join(installDir, "go.mod")
	if _, err := os.Stat(goModPath); err == nil {
		goBin, err := golang.EnsureGo()
		if err != nil {
			fmt.Printf("Warning: could not ensure Go installation: %v\n", err)
			fmt.Println("Package cloned but not built. Install Go and run 'gop update' to build.")
		} else {
			binPath, err := golang.BuildAndInstall(goBin, installDir, name)
			if err != nil {
				fmt.Printf("Warning: build failed: %v\n", err)
				fmt.Println("Package cloned but not built.")
			} else {
				binaryPath = binPath

				// Ensure PATH includes ~/.gop/bin
				added, err := golang.EnsurePath()
				if err != nil {
					fmt.Printf("Warning: could not update PATH: %v\n", err)
					fmt.Printf("Add %s to your PATH manually.\n", golang.BinDir())
				} else if added {
					golang.PrintPathInstructions()
				}
			}
		}
	} else {
		fmt.Println("Not a Go project (no go.mod). Package cloned only.")
	}

	// Update config
	pkg := config.Package{
		Name:    name,
		Source:  nameOrURL,
		Version: version,
	}
	m.Config.AddPackage(pkg)

	// Update lockfile
	lp := lockfile.LockedPackage{
		Name:           name,
		Source:         gitURL,
		Version:        version,
		ResolvedCommit: result.CommitHash,
		InstalledAt:    time.Now(),
	}
	if binaryPath != "" {
		lp.BinaryPath = binaryPath
	}
	m.LockFile.Set(lp)

	// Save
	if err := config.Save(m.Config, m.ConfigPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	if err := m.LockFile.Save(m.LockPath); err != nil {
		return fmt.Errorf("failed to save lock file: %w", err)
	}

	fmt.Printf("\nInstalled %s @ %s (commit: %s)\n", name, result.Reference, result.CommitHash[:12])
	if binaryPath != "" {
		fmt.Printf("Binary: %s\n", binaryPath)
	}
	return nil
}

func (m *Manager) Remove(name string) error {
	pkg, found := m.Config.FindPackage(name)
	if !found {
		return fmt.Errorf("package %q not found in config", name)
	}

	// Remove source directory
	installDir := filepath.Join(m.BaseDir, m.Config.InstallDir, pkg.Name)
	if err := os.RemoveAll(installDir); err != nil {
		return fmt.Errorf("failed to remove package directory: %w", err)
	}

	// Remove binary from ~/.gop/bin and desktop shortcut
	if locked, ok := m.LockFile.Get(name); ok && locked.BinaryPath != "" {
		if err := os.Remove(locked.BinaryPath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("Warning: could not remove binary %s: %v\n", locked.BinaryPath, err)
		} else {
			fmt.Printf("Removed binary: %s\n", locked.BinaryPath)
		}
	}
	golang.RemoveDesktopShortcut(name)

	// Stop and remove service if registered
	if golang.IsServiceRegistered(name) {
		if err := golang.UnregisterService(name); err != nil {
			fmt.Printf("Warning: could not remove service: %v\n", err)
		}
	}

	// Update config and lockfile
	m.Config.RemovePackage(name)
	m.LockFile.Remove(name)

	if err := config.Save(m.Config, m.ConfigPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}
	if err := m.LockFile.Save(m.LockPath); err != nil {
		return fmt.Errorf("failed to save lock file: %w", err)
	}

	fmt.Printf("Removed package %s\n", name)
	return nil
}

func (m *Manager) Update(name string) error {
	pkg, found := m.Config.FindPackage(name)
	if !found {
		return fmt.Errorf("package %q not found in config", name)
	}

	installDir := filepath.Join(m.BaseDir, m.Config.InstallDir, pkg.Name)
	if _, err := os.Stat(installDir); os.IsNotExist(err) {
		return fmt.Errorf("package %q not installed, use 'gop install' first", name)
	}

	_, token, _ := m.Config.ResolveSource(pkg.Source)
	gc := gitclient.New(token)

	// Get old commit
	oldCommit := ""
	if locked, ok := m.LockFile.Get(name); ok {
		oldCommit = locked.ResolvedCommit
	}

	// Fetch latest
	fmt.Printf("Updating %s...\n", name)
	if err := gc.Fetch(installDir); err != nil {
		return err
	}

	// Checkout version
	var result *gitclient.CloneResult
	var err error
	if pkg.Version != "" {
		result, err = gc.CheckoutVersion(installDir, pkg.Version)
	} else {
		result, err = gc.Pull(installDir)
	}
	if err != nil {
		return err
	}

	// Rebuild if Go project and source changed
	binaryPath := ""
	if oldCommit != result.CommitHash {
		goModPath := filepath.Join(installDir, "go.mod")
		if _, err := os.Stat(goModPath); err == nil {
			goBin, err := golang.EnsureGo()
			if err == nil {
				binPath, err := golang.BuildAndInstall(goBin, installDir, name)
				if err != nil {
					fmt.Printf("Warning: rebuild failed: %v\n", err)
				} else {
					binaryPath = binPath
				}
			}
		}
	}

	// Update lockfile
	lp := lockfile.LockedPackage{
		Name:           name,
		Source:         pkg.Source,
		Version:        pkg.Version,
		ResolvedCommit: result.CommitHash,
		InstalledAt:    time.Now(),
	}
	if binaryPath != "" {
		lp.BinaryPath = binaryPath
	} else if locked, ok := m.LockFile.Get(name); ok {
		lp.BinaryPath = locked.BinaryPath
	}
	m.LockFile.Set(lp)

	if err := m.LockFile.Save(m.LockPath); err != nil {
		return fmt.Errorf("failed to save lock file: %w", err)
	}

	if oldCommit != "" && oldCommit != result.CommitHash {
		fmt.Printf("Updated %s: %s -> %s\n", name, oldCommit[:12], result.CommitHash[:12])
	} else {
		fmt.Printf("Package %s is already up to date (%s)\n", name, result.CommitHash[:12])
	}
	return nil
}

func (m *Manager) UpdateAll() error {
	if len(m.Config.Packages) == 0 {
		fmt.Println("No packages to update.")
		return nil
	}
	var errs []error
	for _, pkg := range m.Config.Packages {
		if err := m.Update(pkg.Name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", pkg.Name, err))
		}
	}
	if len(errs) > 0 {
		fmt.Printf("\n%d package(s) failed to update:\n", len(errs))
		for _, e := range errs {
			fmt.Printf("  - %s\n", e)
		}
		return fmt.Errorf("%d package(s) failed to update", len(errs))
	}
	return nil
}

func (m *Manager) List() []PackageInfo {
	var result []PackageInfo
	for _, pkg := range m.Config.Packages {
		info := PackageInfo{
			Name:    pkg.Name,
			Source:  pkg.Source,
			Version: pkg.Version,
		}
		installDir := filepath.Join(m.BaseDir, m.Config.InstallDir, pkg.Name)
		if _, err := os.Stat(installDir); err == nil {
			info.Installed = true
		}
		if locked, ok := m.LockFile.Get(pkg.Name); ok {
			info.ResolvedCommit = locked.ResolvedCommit
			info.InstalledAt = locked.InstalledAt
			info.BinaryPath = locked.BinaryPath
		}
		result = append(result, info)
	}
	return result
}
