package cmd

import (
	"fmt"
	"strings"

	"github.com/corbie79/gop/internal/config"
	"github.com/spf13/cobra"
)

var (
	installVersion string
	installName    string
)

var installCmd = &cobra.Command{
	Use:   "install <package>",
	Short: "Install a package from a Git repository or by searching registries",
	Long: `Install a Go package by URL, registry shorthand, or name search.

If a plain name is given (no URL, no registry:path), gop searches all
configured registries in order and installs the first match.

Examples:
  gop install https://github.com/org/repo.git          # direct URL
  gop install github:spf13/cobra                        # registry:path
  gop install mylab:group/project                       # GitLab shorthand
  gop install cobra                                     # search all registries
  gop install cobra --version v1.8.1                    # search + pin version
  gop install cobra --name my-cobra                     # search + custom name`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		input := args[0]

		// Direct URL or registry:path -> install directly
		if isDirectSource(input) {
			return mgr.Install(input, installVersion, installName)
		}

		// Plain name -> search registries in order
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

			// Find exact or best match
			for _, result := range results {
				name := result.Name
				// Exact match: repo name matches input
				parts := strings.Split(name, "/")
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

			// No exact match - try first result if query is specific enough
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

		return fmt.Errorf("package %q not found in any registry.\n\nTry with a full URL:\n  gop install https://github.com/org/%s.git", input, input)
	},
}

// isDirectSource returns true if the input is a URL or registry:path shorthand.
func isDirectSource(input string) bool {
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return true
	}
	if strings.HasPrefix(input, "git@") {
		return true
	}
	if strings.Contains(input, ":") {
		return true
	}
	// Contains / means it's likely org/repo format, not a plain name
	if strings.Contains(input, "/") {
		return true
	}
	return false
}

func init() {
	installCmd.Flags().StringVarP(&installVersion, "version", "v", "", "version (tag, branch, or commit SHA)")
	installCmd.Flags().StringVarP(&installName, "name", "n", "", "override package name")
}
