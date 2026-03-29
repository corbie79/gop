package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"
)

var (
	createModule  string
	createLicense string
	createNoGit   bool
)

var createCmd = &cobra.Command{
	Use:   "create <package-name>",
	Short: "Create a new Go package project with boilerplate and git repo",
	Long: `Scaffold a new Go package project with standard directory structure,
boilerplate files, and initialized git repository.

Creates:
  <name>/
    ├── main.go
    ├── go.mod
    ├── README.md
    ├── LICENSE
    ├── .gitignore
    ├── Makefile
    ├── gop.yaml
    ├── cmd/
    │   └── root.go
    ├── internal/
    │   └── version/
    │       └── version.go
    └── pkg/
        └── .gitkeep

Examples:
  gop create my-tool
  gop create my-tool --module github.com/myorg/my-tool
  gop create my-lib --license apache
  gop create my-tool --no-git`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		return createPackage(name)
	},
}

func createPackage(name string) error {
	// Validate name
	if name == "" || strings.ContainsAny(name, " /\\:*?\"<>|") {
		return fmt.Errorf("invalid package name: %q", name)
	}

	baseDir, err := os.Getwd()
	if err != nil {
		return err
	}
	projectDir := filepath.Join(baseDir, name)

	if _, err := os.Stat(projectDir); err == nil {
		return fmt.Errorf("directory %q already exists", name)
	}

	// Determine module path
	modulePath := createModule
	if modulePath == "" {
		modulePath = "github.com/user/" + name
	}

	fmt.Printf("Creating package %s...\n", name)

	// Create directory structure
	dirs := []string{
		"cmd",
		"internal/version",
		"pkg",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(projectDir, d), 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", d, err)
		}
	}

	// Template data
	data := map[string]string{
		"Name":       name,
		"Module":     modulePath,
		"Year":       fmt.Sprintf("%d", time.Now().Year()),
		"License":    createLicense,
		"GoVersion":  "1.22",
		"CreateDate": time.Now().Format("2006-01-02"),
	}

	// Write all boilerplate files
	files := map[string]string{
		"go.mod":                  tmplGoMod,
		"main.go":                tmplMainGo,
		"cmd/root.go":            tmplCmdRoot,
		"internal/version/version.go": tmplVersion,
		"README.md":              tmplReadme,
		".gitignore":             tmplGitignore,
		"Makefile":               tmplMakefile,
		"gop.yaml":               tmplGopYaml,
		"pkg/.gitkeep":           "",
	}

	// Add manifest file for package registry
	files["gop-manifest.yaml"] = tmplManifest

	// License
	switch strings.ToLower(createLicense) {
	case "mit":
		files["LICENSE"] = tmplLicenseMIT
	case "apache", "apache-2.0":
		files["LICENSE"] = tmplLicenseApache
	default:
		files["LICENSE"] = tmplLicenseMIT
	}

	for relPath, tmplContent := range files {
		fullPath := filepath.Join(projectDir, relPath)
		if tmplContent == "" {
			os.WriteFile(fullPath, []byte(""), 0644)
			continue
		}
		if err := writeTemplate(fullPath, tmplContent, data); err != nil {
			return fmt.Errorf("failed to write %s: %w", relPath, err)
		}
	}

	// Init git repo
	if !createNoGit {
		if err := initGitRepo(projectDir); err != nil {
			fmt.Printf("Warning: git init failed: %v\n", err)
		}
	}

	fmt.Printf("\nPackage created: %s/\n", name)
	fmt.Printf("Module: %s\n\n", modulePath)
	fmt.Println("Next steps:")
	fmt.Printf("  cd %s\n", name)
	fmt.Println("  go mod tidy")
	fmt.Println("  go run .")
	fmt.Println("")
	fmt.Println("To publish to a registry:")
	fmt.Println("  gop publish --registry <name>")

	return nil
}

func writeTemplate(path, tmpl string, data map[string]string) error {
	t, err := template.New("").Parse(tmpl)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return t.Execute(f, data)
}

func initGitRepo(dir string) error {
	cmds := [][]string{
		{"git", "init"},
		{"git", "add", "."},
		{"git", "commit", "-m", "Initial commit - scaffolded by gop create"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s failed: %w", args[0], err)
		}
	}
	fmt.Println("Initialized git repository with initial commit.")
	return nil
}

// ===================== Templates =====================

var tmplGoMod = `module {{.Module}}

go {{.GoVersion}}
`

var tmplMainGo = `package main

import (
	"os"

	"{{.Module}}/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
`

var tmplCmdRoot = `package cmd

import (
	"fmt"

	"{{.Module}}/internal/version"
)

// Execute runs the root command.
func Execute() error {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("{{.Name}} %s\n", version.Version)
		return nil
	}
	fmt.Println("{{.Name}} is ready. Edit cmd/root.go to add commands.")
	return nil
}
`

// Fix: cmd/root.go needs os import
func init() {
	tmplCmdRoot = `package cmd

import (
	"fmt"
	"os"

	"{{.Module}}/internal/version"
)

// Execute runs the root command.
func Execute() error {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("{{.Name}} %s\n", version.Version)
		return nil
	}
	fmt.Println("{{.Name}} is ready. Edit cmd/root.go to add commands.")
	return nil
}
`

	createCmd.Flags().StringVar(&createModule, "module", "", "Go module path (default: github.com/user/<name>)")
	createCmd.Flags().StringVar(&createLicense, "license", "mit", "license type: mit, apache")
	createCmd.Flags().BoolVar(&createNoGit, "no-git", false, "skip git init")
}

var tmplVersion = `package version

var (
	Version   = "0.1.0"
	BuildDate = "unknown"
	GitCommit = "unknown"
)
`

var tmplReadme = `# {{.Name}}

> Created with [gop](https://github.com/corbie79/gop) on {{.CreateDate}}

## Install

` + "```bash" + `
gop install <your-git-url>
` + "```" + `

## Build

` + "```bash" + `
make build
` + "```" + `

## Usage

` + "```bash" + `
./{{.Name}}
./{{.Name}} version
` + "```" + `

## License

{{.License}}
`

var tmplGitignore = `# Binary
/{{.Name}}
*.exe

# Go
vendor/
*.test
*.out

# IDE
.idea/
.vscode/
*.swp
*.swo

# OS
.DS_Store
Thumbs.db

# gop
.gop_packages/
`

var tmplMakefile = `APP_NAME := {{.Name}}
MODULE := {{.Module}}
VERSION := $(shell cat internal/version/version.go | grep 'Version' | head -1 | cut -d'"' -f2)
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) \
           -X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE) \
           -X $(MODULE)/internal/version.GitCommit=$(GIT_COMMIT)

.PHONY: build clean test run

build:
	go build -ldflags "$(LDFLAGS)" -o $(APP_NAME) .

clean:
	rm -f $(APP_NAME)

test:
	go test ./...

run: build
	./$(APP_NAME)
`

var tmplGopYaml = `install_dir: .gop_packages
packages: []
`

var tmplManifest = `# gop package manifest
# This file describes your package for registry publishing.
name: {{.Name}}
module: {{.Module}}
version: 0.1.0
description: ""
license: {{.License}}
authors: []
repository: ""
keywords: []
build:
  entry: "."
  targets:
    - os: linux
      arch: amd64
    - os: darwin
      arch: amd64
    - os: darwin
      arch: arm64
    - os: windows
      arch: amd64
`

var tmplLicenseMIT = `MIT License

Copyright (c) {{.Year}}

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`

var tmplLicenseApache = `                              Apache License
                        Version 2.0, January 2004
                     http://www.apache.org/licenses/

Copyright (c) {{.Year}}

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
`
