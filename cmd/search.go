package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/corbie79/gop/internal/registry"
	"github.com/spf13/cobra"
)

var searchRegistry string

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search packages in GitLab registries",
	Long: `Search for packages in configured GitLab registries.

Examples:
  gop search my-package
  gop search my-package --registry mylab`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		query := args[0]
		found := false

		for _, reg := range mgr.Config.Registries {
			if reg.Type != "gitlab" {
				continue
			}
			if searchRegistry != "" && reg.Name != searchRegistry {
				continue
			}

			found = true
			glReg, err := registry.NewGitLabRegistry(reg.URL, reg.Token, reg.GroupID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to connect to registry %s: %v\n", reg.Name, err)
				continue
			}

			results, err := glReg.Search(query)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: search failed on %s: %v\n", reg.Name, err)
				continue
			}

			if len(results) == 0 {
				fmt.Printf("No results found in %s for %q\n", reg.Name, query)
				continue
			}

			fmt.Printf("\nResults from %s:\n", reg.Name)
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tDESCRIPTION\tSTARS\tLAST UPDATE\tCLONE URL")
			for _, r := range results {
				desc := r.Description
				if len(desc) > 50 {
					desc = desc[:47] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", r.Name, desc, r.Stars, r.LastUpdate, r.CloneURL)
			}
			w.Flush()
		}

		if !found {
			if searchRegistry != "" {
				return fmt.Errorf("registry %q not found or is not a GitLab registry", searchRegistry)
			}
			fmt.Println("No GitLab registries configured. Add one with:")
			fmt.Println("  gop config add-registry --name mylab --type gitlab --url https://gitlab.com --token YOUR_TOKEN")
		}

		return nil
	},
}

func init() {
	searchCmd.Flags().StringVar(&searchRegistry, "registry", "", "search specific registry")
}
