package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/corbie79/gop/internal/config"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new gop project",
	Long:  "Creates a gop.yaml configuration file and .gop_packages directory in the current directory.",
	RunE: func(cmd *cobra.Command, args []string) error {
		baseDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get working directory: %w", err)
		}

		configPath := filepath.Join(baseDir, config.DefaultConfigFile)
		if _, err := os.Stat(configPath); err == nil {
			return fmt.Errorf("gop.yaml already exists in current directory")
		}

		cfg := config.DefaultConfig()

		// Create config file
		if err := config.Save(cfg, configPath); err != nil {
			return err
		}

		// Create install directory
		installDir := filepath.Join(baseDir, cfg.InstallDir)
		if err := os.MkdirAll(installDir, 0755); err != nil {
			return fmt.Errorf("failed to create install directory: %w", err)
		}

		// Create .gitkeep in install dir
		gitkeep := filepath.Join(installDir, ".gitkeep")
		os.WriteFile(gitkeep, []byte(""), 0644)

		// Create empty lock file
		lockPath := filepath.Join(baseDir, config.DefaultLockFile)
		os.WriteFile(lockPath, []byte("packages: []\n"), 0644)

		fmt.Println("Initialized gop project:")
		fmt.Printf("  - Created %s\n", config.DefaultConfigFile)
		fmt.Printf("  - Created %s\n", config.DefaultLockFile)
		fmt.Printf("  - Created %s/\n", cfg.InstallDir)
		fmt.Println("\nAdd registries with: gop config add-registry")
		fmt.Println("Install packages with: gop install <git-url>")

		return nil
	},
}
