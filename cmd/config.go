package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/corbie79/gop/internal/config"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage gop configuration",
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "List current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		cfg := mgr.Config
		fmt.Printf("Install directory: %s\n", cfg.InstallDir)
		fmt.Printf("Config file: %s\n", mgr.ConfigPath)
		fmt.Printf("Lock file: %s\n", mgr.LockPath)

		if len(cfg.Registries) > 0 {
			fmt.Println("\nRegistries:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tURL\tORG/GROUP\tAUTH")
			for _, r := range cfg.Registries {
				scope := "-"
				if r.Org != "" {
					scope = r.Org
				} else if r.GroupID > 0 {
					scope = fmt.Sprintf("group:%d", r.GroupID)
				}
				auth := "none"
				if r.Token != "" {
					auth = "token"
				}
				if r.ClientID != "" {
					auth += "+oauth"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.Type, r.URL, scope, auth)
			}
			w.Flush()
		} else {
			fmt.Println("\nNo registries configured.")
		}

		return nil
	},
}

var (
	regName    string
	regType    string
	regURL     string
	regToken   string
	regGroupID int
	regOrg     string
	regLocal   bool
)

var configAddRegistryCmd = &cobra.Command{
	Use:   "add-registry",
	Short: "Add a GitHub, GitLab, or Git registry",
	Long: `Add a registry to the configuration. Supports multiple registries.

Types:
  github  - GitHub or GitHub Enterprise (search via API, device flow login)
  gitlab  - GitLab (search via API, OAuth login)
  git     - Generic Git server (clone only, no search)

Examples:
  gop config add-registry --name github --type github --url https://github.com
  gop config add-registry --name gh-work --type github --url https://github.com --org my-company
  gop config add-registry --name mylab --type gitlab --url https://gitlab.com
  gop config add-registry --name mylab --type gitlab --url https://gitlab.example.com --group-id 123
  gop config add-registry --name internal --type git --url https://git.internal.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if regName == "" || regURL == "" {
			return fmt.Errorf("--name and --url are required")
		}
		if regType == "" {
			regType = config.RegistryTypeGit
		}
		validTypes := map[string]bool{
			config.RegistryTypeGit:    true,
			config.RegistryTypeGitLab: true,
			config.RegistryTypeGitHub: true,
		}
		if !validTypes[regType] {
			return fmt.Errorf("--type must be 'git', 'github', or 'gitlab'")
		}

		var cfg *config.Config
		var configPath string

		if regLocal {
			mgr, err := loadManager()
			if err != nil {
				return err
			}
			cfg = mgr.Config
			configPath = mgr.ConfigPath
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			configPath = filepath.Join(home, config.GlobalConfigDir, config.GlobalConfigFile)
			cfg, _ = config.Load(configPath)
			if cfg == nil {
				cfg = config.DefaultConfig()
			}
		}

		reg := config.Registry{
			Name:    regName,
			Type:    regType,
			URL:     regURL,
			Token:   regToken,
			GroupID: regGroupID,
			Org:     regOrg,
		}

		if _, found := cfg.FindRegistry(regName); found {
			return fmt.Errorf("registry %q already exists (use remove-registry first)", regName)
		}

		cfg.Registries = append(cfg.Registries, reg)

		if err := config.Save(cfg, configPath); err != nil {
			return err
		}

		fmt.Printf("Added registry %q (%s) -> %s\n", regName, regType, regURL)

		// Helpful next step hints
		switch regType {
		case config.RegistryTypeGitHub:
			fmt.Printf("\nNext: gop login --registry %s\n", regName)
		case config.RegistryTypeGitLab:
			fmt.Printf("\nNext: gop login --registry %s\n", regName)
		}

		return nil
	},
}

var configRemoveRegistryCmd = &cobra.Command{
	Use:   "remove-registry <name>",
	Short: "Remove a registry",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		name := args[0]
		found := false
		newRegs := make([]config.Registry, 0, len(mgr.Config.Registries))
		for _, r := range mgr.Config.Registries {
			if r.Name == name {
				found = true
				continue
			}
			newRegs = append(newRegs, r)
		}

		if !found {
			return fmt.Errorf("registry %q not found", name)
		}

		mgr.Config.Registries = newRegs
		if err := config.Save(mgr.Config, mgr.ConfigPath); err != nil {
			return err
		}

		fmt.Printf("Removed registry %q\n", name)
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a configuration value.

Supported keys:
  install_dir  - Directory for installed packages`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		key, value := args[0], args[1]
		switch key {
		case "install_dir":
			mgr.Config.InstallDir = value
		default:
			return fmt.Errorf("unknown config key: %s", key)
		}

		if err := config.Save(mgr.Config, mgr.ConfigPath); err != nil {
			return err
		}

		fmt.Printf("Set %s = %s\n", key, value)
		return nil
	},
}

func init() {
	configAddRegistryCmd.Flags().StringVar(&regName, "name", "", "registry name (unique identifier)")
	configAddRegistryCmd.Flags().StringVar(&regType, "type", "git", "registry type: github, gitlab, or git")
	configAddRegistryCmd.Flags().StringVar(&regURL, "url", "", "registry URL")
	configAddRegistryCmd.Flags().StringVar(&regToken, "token", "", "authentication token")
	configAddRegistryCmd.Flags().IntVar(&regGroupID, "group-id", 0, "GitLab group ID (optional)")
	configAddRegistryCmd.Flags().StringVar(&regOrg, "org", "", "GitHub org to scope searches (optional)")
	configAddRegistryCmd.Flags().BoolVar(&regLocal, "local", false, "save to local project config instead of global")

	configCmd.AddCommand(configListCmd)
	configCmd.AddCommand(configAddRegistryCmd)
	configCmd.AddCommand(configRemoveRegistryCmd)
	configCmd.AddCommand(configSetCmd)
}
