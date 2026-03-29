package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/corbie79/gop/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	publishRegistry string
	publishTag      string
	publishMessage  string
)

// Manifest represents gop-manifest.yaml
type Manifest struct {
	Name        string            `yaml:"name"`
	Module      string            `yaml:"module"`
	Version     string            `yaml:"version"`
	Description string            `yaml:"description"`
	License     string            `yaml:"license"`
	Authors     []string          `yaml:"authors"`
	Repository  string            `yaml:"repository"`
	Keywords    []string          `yaml:"keywords"`
	Build       ManifestBuild     `yaml:"build"`
}

type ManifestBuild struct {
	Entry   string           `yaml:"entry"`
	Targets []ManifestTarget `yaml:"targets"`
}

type ManifestTarget struct {
	OS   string `yaml:"os"`
	Arch string `yaml:"arch"`
}

var publishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish package to a Git registry (GitHub/GitLab)",
	Long: `Push your package to a remote Git repository and register it
in the registry's package system.

This command:
  1. Reads gop-manifest.yaml for package metadata
  2. Tags the release with the manifest version
  3. Pushes code + tag to the remote repository
  4. Registers the package in the registry (GitLab Package Registry / GitHub Release)

Prerequisites:
  - gop-manifest.yaml exists (created by 'gop create')
  - Git remote 'origin' is configured pointing to the registry
  - You are logged in (gop login)

Examples:
  gop publish --registry github
  gop publish --registry mylab --tag v1.2.0
  gop publish --registry github --message "Bug fixes and improvements"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPublish()
	},
}

func runPublish() error {
	// 1. Read manifest
	manifest, err := loadManifest()
	if err != nil {
		return err
	}

	fmt.Printf("Publishing %s v%s...\n\n", manifest.Name, manifest.Version)

	// 2. Find registry
	mgr, err := loadManager()
	if err != nil {
		return err
	}

	var reg *config.Registry
	if publishRegistry != "" {
		r, found := mgr.Config.FindRegistry(publishRegistry)
		if !found {
			return fmt.Errorf("registry %q not found", publishRegistry)
		}
		reg = r
	} else {
		for i := range mgr.Config.Registries {
			t := mgr.Config.Registries[i].Type
			if t == config.RegistryTypeGitHub || t == config.RegistryTypeGitLab {
				reg = &mgr.Config.Registries[i]
				break
			}
		}
		if reg == nil {
			return fmt.Errorf("no registry configured. Add one: gop config add-registry")
		}
	}

	// 3. Validate git state
	if err := validateGitState(); err != nil {
		return err
	}

	// 4. Ensure remote is set
	remoteURL, err := ensureRemote(reg, manifest)
	if err != nil {
		return err
	}
	fmt.Printf("Remote: %s\n", remoteURL)

	// 5. Tag version
	tag := publishTag
	if tag == "" {
		tag = "v" + manifest.Version
	}
	if err := createGitTag(tag, manifest); err != nil {
		return err
	}

	// 6. Push code + tags
	fmt.Println("Pushing to remote...")
	if err := gitPush(); err != nil {
		return err
	}
	if err := gitPushTags(); err != nil {
		return err
	}

	// 7. Create source archive
	archivePath, err := createSourceArchive(manifest, tag)
	if err != nil {
		fmt.Printf("Warning: source archive creation failed: %v\n", err)
	} else {
		defer os.Remove(archivePath)
		fi, _ := os.Stat(archivePath)
		fmt.Printf("Source archive: %s (%.1f KB)\n", filepath.Base(archivePath), float64(fi.Size())/1024)
	}

	// 8. Register in registry with source
	switch reg.Type {
	case config.RegistryTypeGitHub:
		if err := publishGitHubRelease(reg, manifest, tag, archivePath); err != nil {
			fmt.Printf("Warning: GitHub release creation failed: %v\n", err)
			fmt.Println("Code and tag pushed successfully. Create release manually on GitHub.")
		}
	case config.RegistryTypeGitLab:
		if err := publishGitLabRelease(reg, manifest, tag, archivePath); err != nil {
			fmt.Printf("Warning: GitLab release creation failed: %v\n", err)
			fmt.Println("Code and tag pushed successfully. Create release manually on GitLab.")
		}
	default:
		fmt.Println("Code and tag pushed. Generic git registries don't support package registration.")
	}

	fmt.Printf("\nPublished %s %s to %s (%s)\n", manifest.Name, tag, reg.Name, reg.Type)
	return nil
}

func loadManifest() (*Manifest, error) {
	data, err := os.ReadFile("gop-manifest.yaml")
	if err != nil {
		return nil, fmt.Errorf("gop-manifest.yaml not found. Run 'gop create' first or create it manually")
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid gop-manifest.yaml: %w", err)
	}
	if m.Name == "" {
		return nil, fmt.Errorf("gop-manifest.yaml: 'name' is required")
	}
	if m.Version == "" {
		return nil, fmt.Errorf("gop-manifest.yaml: 'version' is required")
	}
	return &m, nil
}

func validateGitState() error {
	// Check if in a git repo
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("not a git repository. Run 'git init' first")
	}

	// Check for uncommitted changes
	cmd = exec.Command("git", "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git status failed: %w", err)
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		return fmt.Errorf("uncommitted changes detected. Commit or stash before publishing:\n%s", string(out))
	}

	return nil
}

func ensureRemote(reg *config.Registry, m *Manifest) (string, error) {
	// Check if origin remote exists
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return strings.TrimSpace(string(out)), nil
	}

	// No origin set - construct from registry + manifest
	repoURL := buildRepoURL(reg, m)
	if repoURL == "" {
		return "", fmt.Errorf("no git remote 'origin' configured. Set it manually:\n  git remote add origin <url>")
	}

	// Try to create repo first
	switch reg.Type {
	case config.RegistryTypeGitHub:
		createGitHubRepo(reg, m)
	case config.RegistryTypeGitLab:
		createGitLabProject(reg, m)
	}

	cmd = exec.Command("git", "remote", "add", "origin", repoURL)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to add remote: %w", err)
	}
	fmt.Printf("Added remote origin -> %s\n", repoURL)

	return repoURL, nil
}

func buildRepoURL(reg *config.Registry, m *Manifest) string {
	if m.Repository != "" {
		return m.Repository
	}
	baseURL := strings.TrimRight(reg.URL, "/")
	if reg.Type == config.RegistryTypeGitHub && baseURL == "https://api.github.com" {
		baseURL = "https://github.com"
	}
	if reg.Org != "" {
		return fmt.Sprintf("%s/%s/%s.git", baseURL, reg.Org, m.Name)
	}
	return ""
}

func createGitTag(tag string, m *Manifest) error {
	// Check if tag already exists
	cmd := exec.Command("git", "tag", "-l", tag)
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) == tag {
		fmt.Printf("Tag %s already exists, skipping.\n", tag)
		return nil
	}

	msg := publishMessage
	if msg == "" {
		msg = fmt.Sprintf("Release %s", tag)
	}

	cmd = exec.Command("git", "tag", "-a", tag, "-m", msg)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create tag %s: %w", tag, err)
	}
	fmt.Printf("Created tag: %s\n", tag)
	return nil
}

func gitPush() error {
	// Get current branch
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))

	// Push with retry
	for attempt := 0; attempt < 4; attempt++ {
		cmd = exec.Command("git", "push", "-u", "origin", branch)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			return nil
		}
		if attempt < 3 {
			wait := time.Duration(1<<uint(attempt+1)) * time.Second
			fmt.Printf("Push failed, retrying in %v...\n", wait)
			time.Sleep(wait)
		}
	}
	return fmt.Errorf("failed to push after 4 attempts")
}

func gitPushTags() error {
	for attempt := 0; attempt < 4; attempt++ {
		cmd := exec.Command("git", "push", "origin", "--tags")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err == nil {
			return nil
		}
		if attempt < 3 {
			wait := time.Duration(1<<uint(attempt+1)) * time.Second
			fmt.Printf("Tag push failed, retrying in %v...\n", wait)
			time.Sleep(wait)
		}
	}
	return fmt.Errorf("failed to push tags after 4 attempts")
}

// ===================== Source Archive =====================

// createSourceArchive creates a tar.gz of the source using git archive.
func createSourceArchive(m *Manifest, tag string) (string, error) {
	archiveName := fmt.Sprintf("%s-%s-source.tar.gz", m.Name, m.Version)
	archivePath := filepath.Join(os.TempDir(), archiveName)

	cmd := exec.Command("git", "archive",
		"--format=tar.gz",
		"--prefix="+m.Name+"-"+m.Version+"/",
		"-o", archivePath,
		tag)
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git archive failed: %w", err)
	}

	return archivePath, nil
}

// ===================== GitHub Release =====================

func publishGitHubRelease(reg *config.Registry, m *Manifest, tag, archivePath string) error {
	if reg.Token == "" {
		return fmt.Errorf("no token configured. Run: gop login --registry %s", reg.Name)
	}

	// Determine owner/repo from remote URL
	owner, repo, err := parseGitHubRemote()
	if err != nil {
		return err
	}

	apiURL := "https://api.github.com"
	if reg.URL != "https://github.com" && reg.URL != "https://api.github.com" {
		apiURL = strings.TrimRight(reg.URL, "/") + "/api/v3"
	}

	body := map[string]interface{}{
		"tag_name":               tag,
		"name":                   fmt.Sprintf("%s %s", m.Name, tag),
		"body":                   buildReleaseBody(m),
		"draft":                  false,
		"prerelease":             false,
		"generate_release_notes": true,
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/repos/%s/%s/releases", apiURL, owner, repo),
		strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	json.Unmarshal(respBody, &result)
	if htmlURL, ok := result["html_url"].(string); ok {
		fmt.Printf("GitHub Release created: %s\n", htmlURL)
	}

	// Attach source archive to the release
	if archivePath != "" {
		releaseID, _ := result["id"].(float64)
		if releaseID > 0 {
			uploadURL := fmt.Sprintf("%s/repos/%s/%s/releases/%d/assets?name=%s",
				apiURL, owner, repo, int64(releaseID),
				url.QueryEscape(fmt.Sprintf("%s-%s-source.tar.gz", m.Name, m.Version)))

			if err := uploadGitHubAsset(uploadURL, reg.Token, archivePath); err != nil {
				fmt.Printf("Warning: source archive upload failed: %v\n", err)
			} else {
				fmt.Printf("Source archive attached to GitHub Release.\n")
			}
		}
	}

	return nil
}

func uploadGitHubAsset(uploadURL, token, filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", uploadURL, f)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	req.ContentLength = fi.Size()

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed (%d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func parseGitHubRemote() (string, string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("no origin remote")
	}
	remoteURL := strings.TrimSpace(string(out))

	// Parse: https://github.com/owner/repo.git or git@github.com:owner/repo.git
	remoteURL = strings.TrimSuffix(remoteURL, ".git")
	if strings.Contains(remoteURL, "github.com/") {
		parts := strings.Split(remoteURL, "github.com/")
		if len(parts) == 2 {
			segments := strings.SplitN(parts[1], "/", 2)
			if len(segments) == 2 {
				return segments[0], segments[1], nil
			}
		}
	}
	if strings.Contains(remoteURL, "github.com:") {
		parts := strings.Split(remoteURL, "github.com:")
		if len(parts) == 2 {
			segments := strings.SplitN(parts[1], "/", 2)
			if len(segments) == 2 {
				return segments[0], segments[1], nil
			}
		}
	}

	return "", "", fmt.Errorf("could not parse GitHub owner/repo from remote URL: %s", remoteURL)
}

// ===================== GitLab Release =====================

func publishGitLabRelease(reg *config.Registry, m *Manifest, tag, archivePath string) error {
	if reg.Token == "" {
		return fmt.Errorf("no token configured. Run: gop login --registry %s", reg.Name)
	}

	projectPath, err := parseGitLabRemote(reg)
	if err != nil {
		return err
	}

	apiURL := strings.TrimRight(reg.URL, "/") + "/api/v4"

	// Create release via GitLab API
	body := map[string]interface{}{
		"tag_name":    tag,
		"name":        fmt.Sprintf("%s %s", m.Name, tag),
		"description": buildReleaseBody(m),
	}

	jsonBody, _ := json.Marshal(body)
	encodedPath := url.PathEscape(projectPath)
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/projects/%s/releases", apiURL, encodedPath),
		strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", reg.Token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitLab API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitLab API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	json.Unmarshal(respBody, &result)

	// Upload source archive to GitLab Package Registry
	if archivePath != "" {
		if err := uploadGitLabPackage(reg, m, apiURL, encodedPath, archivePath); err != nil {
			fmt.Printf("Warning: GitLab package upload failed: %v\n", err)
		}

		// Also upload manifest for metadata
		uploadGitLabManifest(reg, m, apiURL, encodedPath)
	}

	if links, ok := result["_links"].(map[string]interface{}); ok {
		if selfLink, ok := links["self"].(string); ok {
			fmt.Printf("GitLab Release created: %s\n", selfLink)
		}
	}

	return nil
}

func uploadGitLabPackage(reg *config.Registry, m *Manifest, apiURL, encodedPath, archivePath string) error {
	// Upload source archive to GitLab Generic Package Registry
	fileName := fmt.Sprintf("%s-%s-source.tar.gz", m.Name, m.Version)
	pkgURL := fmt.Sprintf("%s/projects/%s/packages/generic/%s/%s/%s",
		apiURL, encodedPath, m.Name, m.Version, fileName)

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	fi, _ := f.Stat()

	req, err := http.NewRequest("PUT", pkgURL, f)
	if err != nil {
		return err
	}
	req.Header.Set("PRIVATE-TOKEN", reg.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = fi.Size()

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitLab API error (%d): %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Source uploaded to GitLab Package Registry: %s v%s (%s)\n", m.Name, m.Version, fileName)
	return nil
}

func uploadGitLabManifest(reg *config.Registry, m *Manifest, apiURL, encodedPath string) {
	// Upload gop-manifest.yaml as package metadata
	manifestData, err := yaml.Marshal(m)
	if err != nil {
		return
	}

	pkgURL := fmt.Sprintf("%s/projects/%s/packages/generic/%s/%s/gop-manifest.yaml",
		apiURL, encodedPath, m.Name, m.Version)

	req, err := http.NewRequest("PUT", pkgURL, bytes.NewReader(manifestData))
	if err != nil {
		return
	}
	req.Header.Set("PRIVATE-TOKEN", reg.Token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(manifestData))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()

	if resp.StatusCode < 400 {
		fmt.Println("Manifest uploaded to GitLab Package Registry.")
	}
}

func parseGitLabRemote(reg *config.Registry) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no origin remote")
	}
	remoteURL := strings.TrimSpace(string(out))
	remoteURL = strings.TrimSuffix(remoteURL, ".git")

	// Extract project path from URL
	baseHost := strings.TrimPrefix(strings.TrimPrefix(reg.URL, "https://"), "http://")
	baseHost = strings.TrimRight(baseHost, "/")

	if idx := strings.Index(remoteURL, baseHost+"/"); idx >= 0 {
		return remoteURL[idx+len(baseHost)+1:], nil
	}
	if idx := strings.Index(remoteURL, baseHost+":"); idx >= 0 {
		return remoteURL[idx+len(baseHost)+1:], nil
	}

	return "", fmt.Errorf("could not parse GitLab project path from remote URL: %s", remoteURL)
}

// ===================== Create Repo via API =====================

func createGitHubRepo(reg *config.Registry, m *Manifest) {
	if reg.Token == "" {
		return
	}

	apiURL := "https://api.github.com"
	if reg.URL != "https://github.com" && reg.URL != "https://api.github.com" {
		apiURL = strings.TrimRight(reg.URL, "/") + "/api/v3"
	}

	body := map[string]interface{}{
		"name":        m.Name,
		"description": m.Description,
		"private":     false,
	}

	var endpoint string
	if reg.Org != "" {
		endpoint = fmt.Sprintf("%s/orgs/%s/repos", apiURL, reg.Org)
	} else {
		endpoint = apiURL + "/user/repos"
	}

	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", endpoint, strings.NewReader(string(jsonBody)))
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 201 {
		fmt.Printf("Created GitHub repository: %s\n", m.Name)
	}
}

func createGitLabProject(reg *config.Registry, m *Manifest) {
	if reg.Token == "" {
		return
	}

	apiURL := strings.TrimRight(reg.URL, "/") + "/api/v4"

	body := map[string]interface{}{
		"name":        m.Name,
		"description": m.Description,
		"visibility":  "private",
	}
	if reg.GroupID > 0 {
		body["namespace_id"] = reg.GroupID
	}

	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", apiURL+"/projects", strings.NewReader(string(jsonBody)))
	req.Header.Set("PRIVATE-TOKEN", reg.Token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 201 {
		fmt.Printf("Created GitLab project: %s\n", m.Name)
	}
}

// ===================== Helpers =====================

func buildReleaseBody(m *Manifest) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## %s v%s\n\n", m.Name, m.Version))
	if m.Description != "" {
		sb.WriteString(m.Description + "\n\n")
	}
	if len(m.Keywords) > 0 {
		sb.WriteString("**Keywords:** " + strings.Join(m.Keywords, ", ") + "\n\n")
	}
	sb.WriteString("### Install\n\n")
	sb.WriteString(fmt.Sprintf("```bash\ngop install %s\n```\n\n", m.Repository))
	sb.WriteString("---\n*Published with [gop](https://github.com/corbie79/gop)*\n")
	return sb.String()
}

func init() {
	publishCmd.Flags().StringVar(&publishRegistry, "registry", "", "target registry")
	publishCmd.Flags().StringVar(&publishTag, "tag", "", "override version tag (default: v<manifest.version>)")
	publishCmd.Flags().StringVar(&publishMessage, "message", "", "tag/release message")
}
