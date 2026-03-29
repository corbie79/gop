package cmd

import (
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update [package-name]",
	Short: "Update installed packages",
	Long:  "Update a specific package or all packages to their latest versions.",
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		if len(args) > 0 {
			return mgr.Update(args[0])
		}
		return mgr.UpdateAll()
	},
}
