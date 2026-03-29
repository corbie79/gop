package cmd

import (
	"github.com/spf13/cobra"
)

var removeCmd = &cobra.Command{
	Use:     "remove <package-name>",
	Aliases: []string{"rm", "uninstall"},
	Short:   "Remove an installed package",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		return mgr.Remove(args[0])
	},
}
