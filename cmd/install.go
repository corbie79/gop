package cmd

import (
	"github.com/spf13/cobra"
)

var (
	installVersion string
	installName    string
)

var installCmd = &cobra.Command{
	Use:   "install <package-url>",
	Short: "Install a package from a Git repository",
	Long: `Install a Go package by cloning from a Git URL or registry reference.

Supports direct URLs and registry:path shorthand for configured registries.

Examples:
  gop install https://github.com/org/repo.git
  gop install https://gitlab.com/group/project.git --version v1.2.0
  gop install git@github.com:org/repo.git --version main
  gop install github:spf13/cobra                    # GitHub registry shorthand
  gop install mylab:group/project                    # GitLab registry shorthand
  gop install github:org/repo --version v1.0 --name custom-name`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}
		return mgr.Install(args[0], installVersion, installName)
	},
}

func init() {
	installCmd.Flags().StringVarP(&installVersion, "version", "v", "", "version (tag, branch, or commit SHA)")
	installCmd.Flags().StringVarP(&installName, "name", "n", "", "override package name")
}
