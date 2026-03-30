package cmd

import (
	"fmt"
	"strings"

	"github.com/corbie79/gop/internal/config"
	"github.com/corbie79/gop/internal/packages"
	"github.com/spf13/cobra"
)

var (
	installVersion string
	installName    string
)

var installCmd = &cobra.Command{
	Use:   "install <package>",
	Short: "Install a package from a Git repository or by searching registries",
	Long: `Install a Go package by URL, registry shorthand, alias, or name search.

Input formats (in resolution order):
  1. Full URL          https://github.com/org/repo.git
  2. Registry:path     github:spf13/cobra
  3. org/repo          spf13/cobra  (uses default registry)
  4. Plain name        cobra  (searches all registries)

Set a default registry to use org/repo shorthand:
  gop config set default_registry github

Examples:
  gop install https://github.com/org/repo.git          # direct URL
  gop install github:spf13/cobra                        # registry:path
  gop install spf13/cobra                               # org/repo via default registry
  gop install cobra                                     # search all registries
  gop install cobra --version v1.8.1                    # search + pin version`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		input := args[0]

		// 1. Full URL or registry:path -> install directly
		if isURL(input) || hasRegistryPrefix(input, mgr.Config) {
			return mgr.Install(input, installVersion, installName)
		}

		// 2. org/repo format -> resolve via default registry
		if strings.Contains(input, "/") && !strings.Contains(input, "://") {
			return installFromShortPath(mgr, input)
		}

		// 3. Plain name -> search registries in order
		return installBySearch(mgr, input)
	},
}

// installFromShortPath handles "org/repo" by resolving via default registry.
func installFromShortPath(mgr *packages.Manager, input string) error {
	reg := mgr.Config.GetDefaultRegistry()
	if reg == nil {
		return fmt.Errorf("%q looks like org/repo but no default registry configured.\n\nSet one:\n  gop config set default_registry github\n\nOr use full form:\n  gop install github:%s", input, input)
	}

	// Build clone URL from registry base URL + path
	source := reg.Name + ":" + input
	fmt.Printf("Resolving %s via registry %q (%s)\n\n", input, reg.Name, reg.Type)
	return mgr.Install(source, installVersion, installName)
}

// installBySearch searches all registries for a package by name.
func installBySearch(mgr *packages.Manager, input string) error {
	fmt.Printf("Searching registries for %q...\n\n", input)

	for _, reg := range mgr.Config.Registries {
		if reg.Type != config.RegistryTypeGitHub && reg.Type != config.RegistryTypeGitLab {
			continue
		}

		r, err := createRegistry(reg)
		if err != nil {
			continue
		}

		results, err := r.Search(input)
		if err != nil {
			continue
		}

		// Find exact match first (repo name == input)
		for _, result := range results {
			parts := strings.Split(result.Name, "/")
			repoName := parts[len(parts)-1]

			if !strings.EqualFold(repoName, input) {
				continue
			}

			fmt.Printf("Found: %s (%s) [%s]\n", result.Name, result.Description, reg.Name)
			fmt.Printf("       %s\n\n", result.CloneURL)

			source := result.CloneURL
			if source == "" {
				source = result.URL + ".git"
			}
			return mgr.Install(source, installVersion, installName)
		}

		// No exact match - use first result
		if len(results) > 0 {
			result := results[0]
			fmt.Printf("Best match: %s (%s) [%s]\n", result.Name, result.Description, reg.Name)
			fmt.Printf("            %s\n\n", result.CloneURL)

			source := result.CloneURL
			if source == "" {
				source = result.URL + ".git"
			}
			return mgr.Install(source, installVersion, installName)
		}
	}

	return fmt.Errorf("package %q not found in any registry.\n\nTry:\n  gop install https://github.com/org/%s.git\n  gop install github:org/%s", input, input, input)
}

func isURL(input string) bool {
	return strings.HasPrefix(input, "http://") ||
		strings.HasPrefix(input, "https://") ||
		strings.HasPrefix(input, "git@")
}

// hasRegistryPrefix checks if input is "registryname:path" where registryname is a known registry.
func hasRegistryPrefix(input string, cfg *config.Config) bool {
	if !strings.Contains(input, ":") {
		return false
	}
	parts := strings.SplitN(input, ":", 2)
	_, found := cfg.FindRegistry(parts[0])
	return found
}

func init() {
	installCmd.Flags().StringVarP(&installVersion, "version", "v", "", "version (tag, branch, or commit SHA)")
	installCmd.Flags().StringVarP(&installName, "name", "n", "", "override package name")
}
