package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/corbie79/gop/internal/config"
	"github.com/corbie79/gop/internal/lockfile"
	"github.com/corbie79/gop/internal/packages"
	"github.com/spf13/cobra"
)

var (
	cfgFile string
	manager *packages.Manager
)

var rootCmd = &cobra.Command{
	Use:   "gop",
	Short: "Go Package Manager with Git/GitLab integration",
	Long: `gop is a Go package installation manager that supports
installing packages from Git repositories (GitHub, GitLab, or any Git URL)
and searching packages via GitLab registries.`,
	SilenceUsage: true,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./gop.yaml)")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(removeCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(searchCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(loginCmd)
}

func loadManager() (*packages.Manager, error) {
	if manager != nil {
		return manager, nil
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	configPath := cfgFile
	if configPath == "" {
		configPath = filepath.Join(baseDir, config.DefaultConfigFile)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config (run 'gop init' first): %w", err)
	}

	// Merge global config
	globalCfg, err := config.LoadGlobal()
	if err == nil && globalCfg != nil {
		cfg.Merge(globalCfg)
	}

	lockPath := filepath.Join(baseDir, config.DefaultLockFile)
	lf, err := lockfile.Load(lockPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load lock file: %w", err)
	}

	manager = packages.NewManager(cfg, lf, configPath, lockPath, baseDir)
	return manager, nil
}
