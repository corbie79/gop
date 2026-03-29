package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/corbie79/gop/internal/config"
	"github.com/corbie79/gop/internal/registry"
	"github.com/spf13/cobra"
)

var searchRegistry string

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search packages across all registries",
	Long: `Search for packages in configured GitHub and GitLab registries.

Searches all registries by default, or a specific one with --registry.

Examples:
  gop search cobra                          # search all registries
  gop search cobra --registry github        # search GitHub only
  gop search my-lib --registry mylab        # search specific GitLab`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		query := args[0]
		found := false

		for _, reg := range mgr.Config.Registries {
			if reg.Type != config.RegistryTypeGitHub && reg.Type != config.RegistryTypeGitLab {
				continue
			}
			if searchRegistry != "" && reg.Name != searchRegistry {
				continue
			}

			found = true

			r, err := createRegistry(reg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to connect to %s (%s): %v\n", reg.Name, reg.Type, err)
				continue
			}

			results, err := r.Search(query)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: search failed on %s: %v\n", reg.Name, err)
				continue
			}

			if len(results) == 0 {
				fmt.Printf("[%s] No results for %q\n", reg.Name, query)
				continue
			}

			fmt.Printf("\n[%s] (%s) %d results:\n", reg.Name, reg.Type, len(results))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  NAME\tDESCRIPTION\tSTARS\tUPDATED\tCLONE URL")
			for _, r := range results {
				desc := r.Description
				if len(desc) > 50 {
					desc = desc[:47] + "..."
				}
				fmt.Fprintf(w, "  %s\t%s\t%d\t%s\t%s\n", r.Name, desc, r.Stars, r.LastUpdate, r.CloneURL)
			}
			w.Flush()
		}

		if !found {
			if searchRegistry != "" {
				return fmt.Errorf("registry %q not found or not a GitHub/GitLab registry", searchRegistry)
			}
			fmt.Println("No searchable registries configured. Add one:")
			fmt.Println("  gop config add-registry --name github --type github --url https://github.com")
			fmt.Println("  gop config add-registry --name mylab --type gitlab --url https://gitlab.com")
		}

		return nil
	},
}

func createRegistry(reg config.Registry) (registry.Registry, error) {
	switch reg.Type {
	case config.RegistryTypeGitHub:
		return registry.NewGitHubRegistry(reg.URL, reg.Token, reg.Org), nil
	case config.RegistryTypeGitLab:
		return registry.NewGitLabRegistry(reg.URL, reg.Token, reg.GroupID)
	default:
		return nil, fmt.Errorf("unsupported registry type: %s", reg.Type)
	}
}

func init() {
	searchCmd.Flags().StringVar(&searchRegistry, "registry", "", "search specific registry only")
}
