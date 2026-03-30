package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/corbie79/gop/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var (
	publishRegistry   string
	publishTag        string
	publishMessage    string
	publishPath       string
	publishVisibility string
)

// Manifest represents gop-manifest.yaml
type Manifest struct {
	Name        string        `yaml:"name"`
	Module      string        `yaml:"module"`
	Version     string        `yaml:"version"`
	Description string        `yaml:"description"`
	License     string        `yaml:"license"`
	Authors     []string      `yaml:"authors"`
	Repository  string        `yaml:"repository"`
	Keywords    []string      `yaml:"keywords"`
	Build       ManifestBuild `yaml:"build"`
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
	Long: `Push source code to a remote Git repository and register a release.

The Git repository IS the package source. Users install via git clone.
  - Public repos: anyone can 'gop install <url>'
  - Private repos: requires 'gop login' first

This command:
  1. Reads gop-manifest.yaml for package metadata
  2. Creates remote repository if needed (via API)
  3. Pushes all source code + version tag
  4. Creates a Release (GitHub Release / GitLab Release)

The release includes the git clone URL so users can install with:
  gop install <clone-url> --version <tag>

Use --path to specify the exact location on GitLab (group/subgroup):
  gop publish --registry mylab --path team/backend/my-tool

Examples:
  gop publish --registry github
  gop publish --registry mylab --tag v1.2.0
  gop publish --registry mylab --path infra/tools/my-tool
  gop publish --registry mylab --path team/backend/api --visibility public
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

	// 4. Ensure remote is set (creates repo via API if needed)
	remoteURL, err := ensureRemote(reg, manifest)
	if err != nil {
		return err
	}

	// Update manifest repository field if empty
	if manifest.Repository == "" {
		manifest.Repository = remoteURL
	}

	fmt.Printf("Repository: %s\n", remoteURL)

	// 5. Tag version
	tag := publishTag
	if tag == "" {
		tag = "v" + manifest.Version
	}
	if err := createGitTag(tag, manifest); err != nil {
		return err
	}

	// 6. Push source code + tags to git repository
	fmt.Println("Pushing source to repository...")
	if err := gitPush(); err != nil {
		return err
	}
	if err := gitPushTags(); err != nil {
		return err
	}
	fmt.Println("Source code pushed successfully.")

	// 7. Create release (metadata pointing to the git repo)
	switch reg.Type {
	case config.RegistryTypeGitHub:
		if err := publishGitHubRelease(reg, manifest, tag); err != nil {
			fmt.Printf("Warning: GitHub release creation failed: %v\n", err)
			fmt.Println("Source is pushed. Create release manually on GitHub.")
		}
	case config.RegistryTypeGitLab:
		if err := publishGitLabRelease(reg, manifest, tag); err != nil {
			fmt.Printf("Warning: GitLab release creation failed: %v\n", err)
			fmt.Println("Source is pushed. Create release manually on GitLab.")
		}
	default:
		fmt.Println("Source pushed to git repository.")
	}

	cloneURL := remoteURL
	fmt.Printf("\nPublished %s %s to %s (%s)\n", manifest.Name, tag, reg.Name, reg.Type)
	fmt.Println("\nInstall with:")
	fmt.Printf("  gop install %s --version %s\n", cloneURL, tag)
	if publishRegistry != "" {
		fmt.Printf("  gop install %s:%s --version %s\n", reg.Name, repoPathFromURL(remoteURL), tag)
	}

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
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("not a git repository. Run 'git init' first")
	}

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
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return strings.TrimSpace(string(out)), nil
	}

	// No origin - construct URL and create repo via API
	repoURL := buildRepoURL(reg, m)
	if repoURL == "" {
		// For GitHub without org/path: try to get username and create personal repo
		if reg.Type == config.RegistryTypeGitHub && reg.Token != "" {
			username := getGitHubUsername(reg)
			if username != "" {
				baseURL := strings.TrimRight(reg.URL, "/")
				if baseURL == "https://api.github.com" {
					baseURL = "https://github.com"
				}
				repoURL = fmt.Sprintf("%s/%s/%s.git", baseURL, username, m.Name)
			}
		}
		if repoURL == "" {
			return "", fmt.Errorf("no git remote 'origin' and cannot determine repo URL.\n\nSet it manually:\n  git remote add origin <url>\n\nOr use --path: gop publish --registry %s --path <owner>/<repo>", reg.Name)
		}
	}

	// Create repository on the remote
	fmt.Printf("Creating repository on %s...\n", reg.Name)
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

	// --path flag takes priority: exact path like group/subgroup/project
	if publishPath != "" {
		return fmt.Sprintf("%s/%s.git", baseURL, publishPath)
	}
	if reg.Org != "" {
		return fmt.Sprintf("%s/%s/%s.git", baseURL, reg.Org, m.Name)
	}
	return ""
}

func createGitTag(tag string, m *Manifest) error {
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
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))

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

// ===================== GitHub Release =====================

func publishGitHubRelease(reg *config.Registry, m *Manifest, tag string) error {
	if reg.Token == "" {
		return fmt.Errorf("no token configured. Run: gop login --registry %s", reg.Name)
	}

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
		"body":                   buildReleaseBody(m, tag),
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

	return nil
}

func parseGitHubRemote() (string, string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("no origin remote")
	}
	remoteURL := strings.TrimSpace(string(out))
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

func publishGitLabRelease(reg *config.Registry, m *Manifest, tag string) error {
	if reg.Token == "" {
		return fmt.Errorf("no token configured. Run: gop login --registry %s", reg.Name)
	}

	projectPath, err := parseGitLabRemote(reg)
	if err != nil {
		return err
	}

	apiURL := strings.TrimRight(reg.URL, "/") + "/api/v4"

	body := map[string]interface{}{
		"tag_name":    tag,
		"name":        fmt.Sprintf("%s %s", m.Name, tag),
		"description": buildReleaseBody(m, tag),
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

	if links, ok := result["_links"].(map[string]interface{}); ok {
		if selfLink, ok := links["self"].(string); ok {
			fmt.Printf("GitLab Release created: %s\n", selfLink)
		}
	}

	return nil
}

func parseGitLabRemote(reg *config.Registry) (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("no origin remote")
	}
	remoteURL := strings.TrimSpace(string(out))
	remoteURL = strings.TrimSuffix(remoteURL, ".git")

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
		fmt.Println("Warning: no token. Cannot create GitHub repository.")
		return
	}

	apiURL := "https://api.github.com"
	if reg.URL != "https://github.com" && reg.URL != "https://api.github.com" {
		apiURL = strings.TrimRight(reg.URL, "/") + "/api/v3"
	}

	// Default to private unless explicitly set to public
	isPrivate := publishVisibility != "public"

	repoName := m.Name
	var orgName string

	if publishPath != "" {
		parts := strings.Split(publishPath, "/")
		repoName = parts[len(parts)-1]
		if len(parts) >= 2 {
			orgName = parts[0]
		}
	} else if reg.Org != "" {
		orgName = reg.Org
	}

	body := map[string]interface{}{
		"name":        repoName,
		"description": m.Description,
		"private":     isPrivate,
		"auto_init":   false,
	}

	var endpoint string
	if orgName != "" {
		endpoint = fmt.Sprintf("%s/orgs/%s/repos", apiURL, orgName)
		fmt.Printf("Creating repo %q in org %q...\n", repoName, orgName)
	} else {
		endpoint = apiURL + "/user/repos"
		fmt.Printf("Creating personal repo %q...\n", repoName)
	}

	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", endpoint, strings.NewReader(string(jsonBody)))
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Warning: GitHub API request failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case 201:
		var result struct {
			HTMLURL  string `json:"html_url"`
			CloneURL string `json:"clone_url"`
			Private  bool   `json:"private"`
		}
		json.Unmarshal(respBody, &result)
		vis := "public"
		if result.Private {
			vis = "private"
		}
		fmt.Printf("Created GitHub repository: %s (%s)\n", result.HTMLURL, vis)
	case 422:
		// Repository already exists
		fmt.Println("Repository already exists on GitHub.")
	case 404:
		fmt.Printf("Warning: org %q not found or no permission to create repos.\n", orgName)
	default:
		fmt.Printf("Warning: GitHub repo creation failed (%d): %s\n", resp.StatusCode, string(respBody))
	}
}

func createGitLabProject(reg *config.Registry, m *Manifest) {
	if reg.Token == "" {
		return
	}

	apiURL := strings.TrimRight(reg.URL, "/") + "/api/v4"
	client := &http.Client{Timeout: 30 * time.Second}

	visibility := publishVisibility
	if visibility == "" {
		visibility = "private"
	}

	// Determine project name and namespace
	projectName := m.Name
	var namespaceID int

	if publishPath != "" {
		// --path group/subgroup/project-name
		parts := strings.Split(publishPath, "/")
		projectName = parts[len(parts)-1]
		namespacePath := strings.Join(parts[:len(parts)-1], "/")

		if namespacePath != "" {
			// Look up the namespace (group/subgroup) by full path
			nsID, err := lookupGitLabNamespace(apiURL, reg.Token, namespacePath, client)
			if err != nil {
				fmt.Printf("Warning: namespace lookup failed for %q: %v\n", namespacePath, err)
				fmt.Println("Creating project without specific namespace.")
			} else {
				namespaceID = nsID
				fmt.Printf("Found namespace: %s (id: %d)\n", namespacePath, nsID)
			}
		}
	} else if reg.GroupID > 0 {
		namespaceID = reg.GroupID
	}

	body := map[string]interface{}{
		"name":        projectName,
		"description": m.Description,
		"visibility":  visibility,
	}
	if namespaceID > 0 {
		body["namespace_id"] = namespaceID
	}

	jsonBody, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", apiURL+"/projects", strings.NewReader(string(jsonBody)))
	req.Header.Set("PRIVATE-TOKEN", reg.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("Warning: failed to create project: %v\n", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 201 {
		var result struct {
			WebURL       string `json:"web_url"`
			HTTPURLToRepo string `json:"http_url_to_repo"`
		}
		json.Unmarshal(respBody, &result)
		fmt.Printf("Created GitLab project: %s\n", result.WebURL)
	} else if resp.StatusCode == 400 {
		// Possibly already exists
		var errResp struct {
			Message map[string][]string `json:"message"`
		}
		json.Unmarshal(respBody, &errResp)
		if msgs, ok := errResp.Message["name"]; ok {
			for _, msg := range msgs {
				if strings.Contains(msg, "already") {
					fmt.Println("Project already exists on GitLab.")
					return
				}
			}
		}
		fmt.Printf("Warning: GitLab project creation failed (%d): %s\n", resp.StatusCode, string(respBody))
	} else {
		fmt.Printf("Warning: GitLab project creation failed (%d): %s\n", resp.StatusCode, string(respBody))
	}
}

// lookupGitLabNamespace finds a GitLab namespace (group/subgroup) by its full path.
func lookupGitLabNamespace(apiURL, token, namespacePath string, client *http.Client) (int, error) {
	// Try /groups/:path first (works for groups and subgroups)
	encodedPath := url.PathEscape(namespacePath)
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/groups/%s", apiURL, encodedPath), nil)
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		body, _ := io.ReadAll(resp.Body)
		var group struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(body, &group); err == nil && group.ID > 0 {
			return group.ID, nil
		}
	}

	// Fallback: search namespaces
	req2, _ := http.NewRequest("GET",
		fmt.Sprintf("%s/namespaces?search=%s", apiURL, url.QueryEscape(namespacePath)), nil)
	req2.Header.Set("PRIVATE-TOKEN", token)

	resp2, err := client.Do(req2)
	if err != nil {
		return 0, err
	}
	defer resp2.Body.Close()

	body2, _ := io.ReadAll(resp2.Body)
	var namespaces []struct {
		ID       int    `json:"id"`
		FullPath string `json:"full_path"`
	}
	json.Unmarshal(body2, &namespaces)

	for _, ns := range namespaces {
		if ns.FullPath == namespacePath {
			return ns.ID, nil
		}
	}

	return 0, fmt.Errorf("namespace %q not found", namespacePath)
}

// ===================== Helpers =====================

func buildReleaseBody(m *Manifest, tag string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## %s %s\n\n", m.Name, tag))
	if m.Description != "" {
		sb.WriteString(m.Description + "\n\n")
	}
	if len(m.Keywords) > 0 {
		sb.WriteString("**Keywords:** " + strings.Join(m.Keywords, ", ") + "\n\n")
	}

	sb.WriteString("### Install\n\n")
	if m.Repository != "" {
		sb.WriteString(fmt.Sprintf("```bash\n# Clone and build\ngop install %s --version %s\n```\n\n", m.Repository, tag))
	}
	sb.WriteString(fmt.Sprintf("**Module:** `%s`\n", m.Module))
	if m.License != "" {
		sb.WriteString(fmt.Sprintf("**License:** %s\n", m.License))
	}
	sb.WriteString("\n---\n*Published with [gop](https://github.com/corbie79/gop)*\n")
	return sb.String()
}

// getGitHubUsername fetches the authenticated user's login from the GitHub API.
func getGitHubUsername(reg *config.Registry) string {
	apiURL := "https://api.github.com"
	if reg.URL != "https://github.com" && reg.URL != "https://api.github.com" {
		apiURL = strings.TrimRight(reg.URL, "/") + "/api/v3"
	}

	req, _ := http.NewRequest("GET", apiURL+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var user struct {
		Login string `json:"login"`
	}
	json.Unmarshal(body, &user)
	return user.Login
}

// repoPathFromURL extracts "org/repo" from a git URL for shorthand display.
func repoPathFromURL(rawURL string) string {
	u := strings.TrimSuffix(rawURL, ".git")
	// https://github.com/org/repo or git@github.com:org/repo
	for _, sep := range []string{"github.com/", "gitlab.com/", "github.com:", "gitlab.com:"} {
		if idx := strings.Index(u, sep); idx >= 0 {
			return u[idx+len(sep):]
		}
	}
	// Generic: take last two path segments
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return u
}

func init() {
	publishCmd.Flags().StringVar(&publishRegistry, "registry", "", "target registry")
	publishCmd.Flags().StringVar(&publishTag, "tag", "", "override version tag (default: v<manifest.version>)")
	publishCmd.Flags().StringVar(&publishMessage, "message", "", "tag/release message")
	publishCmd.Flags().StringVar(&publishPath, "path", "", "repository path (e.g. group/subgroup/project)")
	publishCmd.Flags().StringVar(&publishVisibility, "visibility", "", "repository visibility: public, private, internal (default: private)")
}
