package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var listJSON bool

var listCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List installed packages",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		pkgs := mgr.List()
		if len(pkgs) == 0 {
			fmt.Println("No packages installed.")
			return nil
		}

		if listJSON {
			data, err := json.MarshalIndent(pkgs, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSOURCE\tVERSION\tCOMMIT\tBINARY\tSTATUS")
		for _, p := range pkgs {
			status := "not installed"
			if p.Installed {
				status = "installed"
			}
			commit := "-"
			if p.ResolvedCommit != "" && len(p.ResolvedCommit) >= 12 {
				commit = p.ResolvedCommit[:12]
			}
			version := p.Version
			if version == "" {
				version = "(default)"
			}
			binary := "-"
			if p.BinaryPath != "" {
				binary = p.BinaryPath
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Name, p.Source, version, commit, binary, status)
		}
		w.Flush()
		return nil
	},
}

func init() {
	listCmd.Flags().BoolVar(&listJSON, "json", false, "output in JSON format")
}
