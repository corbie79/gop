package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/corbie79/gop/internal/config"
	"github.com/corbie79/gop/internal/registry"
	"github.com/spf13/cobra"
)

var (
	loginRegistry string
	loginMethod   string
)

var loginCmd = &cobra.Command{
	Use:   "login [--registry name]",
	Short: "Authenticate with GitHub or GitLab via browser",
	Long: `Open web login page and automatically save the authentication token.

Supported registries:
  github  - Device Flow (recommended for CLI, no server needed)
  gitlab  - OAuth callback flow or Personal Access Token

Methods:
  auto    - Automatically picks best method per registry type (default)
  oauth   - OAuth flow (GitLab callback / GitHub device)
  token   - Opens token creation page, prompts to paste

Examples:
  gop login                                    # login to first registry
  gop login --registry github                  # login to specific registry
  gop login --registry mylab                   # login to GitLab registry
  gop login --method token                     # manual token paste`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		var reg *config.Registry
		if loginRegistry != "" {
			r, found := mgr.Config.FindRegistry(loginRegistry)
			if !found {
				return fmt.Errorf("registry %q not found. List registries: gop config list", loginRegistry)
			}
			reg = r
		} else {
			// Pick first github or gitlab registry
			for i := range mgr.Config.Registries {
				t := mgr.Config.Registries[i].Type
				if t == config.RegistryTypeGitHub || t == config.RegistryTypeGitLab {
					reg = &mgr.Config.Registries[i]
					break
				}
			}
			if reg == nil {
				return fmt.Errorf("no GitHub or GitLab registry configured.\n\nAdd one:\n  gop config add-registry --name github --type github --url https://github.com\n  gop config add-registry --name mylab --type gitlab --url https://gitlab.com")
			}
		}

		method := loginMethod
		if method == "auto" {
			switch reg.Type {
			case config.RegistryTypeGitHub:
				method = "oauth"
			case config.RegistryTypeGitLab:
				if reg.ClientID != "" {
					method = "oauth"
				} else {
					method = "token"
				}
			default:
				method = "token"
			}
		}

		baseURL := strings.TrimRight(reg.URL, "/")

		switch reg.Type {
		case config.RegistryTypeGitHub:
			switch method {
			case "oauth":
				return runGitHubDeviceLogin(baseURL, reg, mgr.ConfigPath, mgr.Config)
			case "token":
				return runGitHubTokenLogin(baseURL, reg, mgr.ConfigPath, mgr.Config)
			}
		case config.RegistryTypeGitLab:
			switch method {
			case "oauth":
				return runGitLabOAuthLogin(baseURL, reg, mgr.ConfigPath, mgr.Config)
			case "token":
				return runGitLabTokenLogin(baseURL, reg, mgr.ConfigPath, mgr.Config)
			}
		default:
			return runGenericTokenLogin(baseURL, reg, mgr.ConfigPath, mgr.Config)
		}

		return nil
	},
}

// ===================== GitHub Device Flow =====================

func runGitHubDeviceLogin(baseURL string, reg *config.Registry, configPath string, cfg *config.Config) error {
	clientID := reg.ClientID
	if clientID == "" {
		fmt.Println("GitHub OAuth client_id is not set.")
		fmt.Println("")
		fmt.Println("Option 1 - Set up OAuth App:")
		fmt.Println("  1. Go to: https://github.com/settings/developers")
		fmt.Println("  2. Create OAuth App -> Enable Device Flow")
		fmt.Printf("  3. Run: gop config set-oauth --registry %s --client-id YOUR_CLIENT_ID\n", reg.Name)
		fmt.Println("  4. Run: gop login")
		fmt.Println("")
		fmt.Println("Option 2 - Use Personal Access Token instead:")
		fmt.Printf("  gop login --registry %s --method token\n", reg.Name)

		openBrowser("https://github.com/settings/developers")
		return nil
	}

	fmt.Println("Starting GitHub Device Flow login...")
	fmt.Println("")

	dc, err := registry.RequestDeviceCode(clientID)
	if err != nil {
		return fmt.Errorf("failed to start device flow: %w", err)
	}

	fmt.Printf("  1. Open: %s\n", dc.VerificationURI)
	fmt.Printf("  2. Enter code: %s\n\n", dc.UserCode)

	if err := openBrowser(dc.VerificationURI); err != nil {
		fmt.Printf("(Open the URL above manually)\n\n")
	}

	fmt.Println("Waiting for authorization...")

	tokenResp, err := registry.PollForToken(clientID, dc.DeviceCode, dc.Interval)
	if err != nil {
		return err
	}

	cfg.UpdateRegistryToken(reg.Name, tokenResp.AccessToken)
	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return postLoginSetup(reg, cfg, configPath)
}

func runGitHubTokenLogin(baseURL string, reg *config.Registry, configPath string, cfg *config.Config) error {
	tokenURL := "https://github.com/settings/tokens/new?description=gop-cli&scopes=repo,read:org"
	if baseURL != "https://github.com" && baseURL != "https://api.github.com" {
		// GitHub Enterprise
		tokenURL = baseURL + "/settings/tokens/new?description=gop-cli&scopes=repo,read:org"
	}

	fmt.Printf("Opening GitHub token page for registry %q...\n\n", reg.Name)
	fmt.Println("Create a token with scopes: repo, read:org")

	if err := openBrowser(tokenURL); err != nil {
		fmt.Printf("Open manually: %s\n", tokenURL)
	}

	return promptAndSaveToken(reg, cfg, configPath)
}

// ===================== GitLab OAuth =====================

func runGitLabOAuthLogin(baseURL string, reg *config.Registry, configPath string, cfg *config.Config) error {
	if reg.ClientID == "" {
		fmt.Println("OAuth client_id is not configured for this registry.")
		fmt.Println("")
		fmt.Printf("1. Go to: %s/-/user_settings/applications\n", baseURL)
		fmt.Println("2. Create an application with:")
		fmt.Println("   - Name: gop")
		fmt.Println("   - Redirect URI: http://127.0.0.1:19287/callback")
		fmt.Println("   - Scopes: read_api, read_repository")
		fmt.Println("3. Then run:")
		fmt.Printf("   gop config set-oauth --registry %s --client-id YOUR_ID --client-secret YOUR_SECRET\n", reg.Name)
		fmt.Println("4. Finally run: gop login")

		openBrowser(baseURL + "/-/user_settings/applications")
		return nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:19287")
	if err != nil {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("failed to start callback server: %w", err)
		}
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	authURL := fmt.Sprintf("%s/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=read_api+read_repository",
		baseURL, url.QueryEscape(reg.ClientID), url.QueryEscape(redirectURI))

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errMsg := r.URL.Query().Get("error_description")
			if errMsg == "" {
				errMsg = "no authorization code received"
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, resultHTML("Login Failed", errMsg, false))
			errCh <- fmt.Errorf("authentication failed: %s", errMsg)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, resultHTML("Login Successful!", "Token saved. You can close this window.", true))
		codeCh <- code
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	fmt.Println("Opening browser for GitLab login...")
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("\nOpen this URL manually:\n%s\n\n", authURL)
	}
	fmt.Println("Waiting for login in browser...")

	select {
	case code := <-codeCh:
		fmt.Println("Login confirmed! Exchanging token...")
		token, err := exchangeGitLabToken(baseURL, reg.ClientID, reg.ClientSecret, code, redirectURI)
		if err != nil {
			return fmt.Errorf("token exchange failed: %w", err)
		}
		cfg.UpdateRegistryToken(reg.Name, token)
		if err := config.Save(cfg, configPath); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}
		return postLoginSetup(reg, cfg, configPath)
	case err := <-errCh:
		return err
	case <-time.After(120 * time.Second):
		return fmt.Errorf("login timed out. Try again: gop login")
	}
}

func runGitLabTokenLogin(baseURL string, reg *config.Registry, configPath string, cfg *config.Config) error {
	tokenURL := baseURL + "/-/user_settings/personal_access_tokens"

	fmt.Printf("Opening GitLab token page for registry %q...\n\n", reg.Name)
	fmt.Println("Create a token with scopes: read_api, read_repository")

	if err := openBrowser(tokenURL); err != nil {
		fmt.Printf("Open manually: %s\n", tokenURL)
	}

	return promptAndSaveToken(reg, cfg, configPath)
}

// ===================== Generic Git =====================

func runGenericTokenLogin(baseURL string, reg *config.Registry, configPath string, cfg *config.Config) error {
	fmt.Printf("Registry %q is type %q.\n\n", reg.Name, reg.Type)
	fmt.Println("Enter your access token for authentication.")
	return promptAndSaveToken(reg, cfg, configPath)
}

// ===================== Shared Helpers =====================

func promptAndSaveToken(reg *config.Registry, cfg *config.Config, configPath string) error {
	fmt.Println("")
	fmt.Print("Paste your token here: ")

	var token string
	fmt.Scanln(&token)
	token = strings.TrimSpace(token)

	if token == "" {
		return fmt.Errorf("no token provided")
	}

	cfg.UpdateRegistryToken(reg.Name, token)
	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	return postLoginSetup(reg, cfg, configPath)
}

type gitlabTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func exchangeGitLabToken(baseURL, clientID, clientSecret, code, redirectURI string) (string, error) {
	data := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	}

	resp, err := http.PostForm(baseURL+"/oauth/token", data)
	if err != nil {
		return "", fmt.Errorf("failed to request token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var tokenResp gitlabTokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	if tokenResp.Error != "" {
		return "", fmt.Errorf("%s: %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("empty access token in response")
	}

	return tokenResp.AccessToken, nil
}

// ===================== Post-Login Auto Setup =====================

// postLoginSetup runs after a successful login:
// 1. Verify token works
// 2. Fetch user info (username, orgs/groups)
// 3. Auto-configure org/group if not set
// 4. Save updated config
func postLoginSetup(reg *config.Registry, cfg *config.Config, configPath string) error {
	fmt.Printf("\nLogin successful! Verifying token...\n")

	switch reg.Type {
	case config.RegistryTypeGitHub:
		return postLoginGitHub(reg, cfg, configPath)
	case config.RegistryTypeGitLab:
		return postLoginGitLab(reg, cfg, configPath)
	default:
		fmt.Printf("Logged in to %q.\n", reg.Name)
		return nil
	}
}

func postLoginGitHub(reg *config.Registry, cfg *config.Config, configPath string) error {
	apiURL := "https://api.github.com"
	if reg.URL != "https://github.com" && reg.URL != "https://api.github.com" {
		apiURL = strings.TrimRight(reg.URL, "/") + "/api/v3"
	}

	// 1. Verify token + get user info
	userReq, _ := http.NewRequest("GET", apiURL+"/user", nil)
	userReq.Header.Set("Authorization", "Bearer "+reg.Token)
	userReq.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(userReq)
	if err != nil {
		fmt.Printf("Warning: could not verify token: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("token is invalid or expired. Try logging in again")
	}

	body, _ := io.ReadAll(resp.Body)
	var user struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	json.Unmarshal(body, &user)

	fmt.Printf("Authenticated as: %s", user.Login)
	if user.Name != "" {
		fmt.Printf(" (%s)", user.Name)
	}
	fmt.Println()

	// 2. Fetch orgs
	orgsReq, _ := http.NewRequest("GET", apiURL+"/user/orgs?per_page=50", nil)
	orgsReq.Header.Set("Authorization", "Bearer "+reg.Token)
	orgsReq.Header.Set("Accept", "application/vnd.github+json")

	resp2, err := client.Do(orgsReq)
	if err == nil && resp2.StatusCode == 200 {
		defer resp2.Body.Close()
		body2, _ := io.ReadAll(resp2.Body)
		var orgs []struct {
			Login string `json:"login"`
		}
		json.Unmarshal(body2, &orgs)

		if len(orgs) > 0 {
			fmt.Printf("Organizations: ")
			orgNames := make([]string, len(orgs))
			for i, o := range orgs {
				orgNames[i] = o.Login
			}
			fmt.Println(strings.Join(orgNames, ", "))

			// Auto-set org if not configured and only one org
			if reg.Org == "" {
				if len(orgs) == 1 {
					reg.Org = orgs[0].Login
					fmt.Printf("Auto-configured org: %s\n", reg.Org)
				} else {
					fmt.Println("\nMultiple orgs found. Set one with:")
					for _, o := range orgs {
						fmt.Printf("  gop config set-org --registry %s --org %s\n", reg.Name, o.Login)
					}
				}
			}
		}
	}

	// 3. Save
	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("\nRegistry %q is ready. You can now:\n", reg.Name)
	fmt.Printf("  gop search <query>               # search packages\n")
	fmt.Printf("  gop install <name>               # install by name\n")
	if reg.Org != "" {
		fmt.Printf("  gop install %s:org/repo       # install from org\n", reg.Name)
	}
	return nil
}

func postLoginGitLab(reg *config.Registry, cfg *config.Config, configPath string) error {
	apiURL := strings.TrimRight(reg.URL, "/") + "/api/v4"

	// 1. Verify token + get user info
	userReq, _ := http.NewRequest("GET", apiURL+"/user", nil)
	userReq.Header.Set("PRIVATE-TOKEN", reg.Token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(userReq)
	if err != nil {
		fmt.Printf("Warning: could not verify token: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("token is invalid or expired. Try logging in again")
	}

	body, _ := io.ReadAll(resp.Body)
	var user struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	json.Unmarshal(body, &user)

	fmt.Printf("Authenticated as: %s", user.Username)
	if user.Name != "" {
		fmt.Printf(" (%s)", user.Name)
	}
	fmt.Println()

	// 2. Fetch groups
	groupsReq, _ := http.NewRequest("GET", apiURL+"/groups?per_page=50&min_access_level=10", nil)
	groupsReq.Header.Set("PRIVATE-TOKEN", reg.Token)

	resp2, err := client.Do(groupsReq)
	if err == nil && resp2.StatusCode == 200 {
		defer resp2.Body.Close()
		body2, _ := io.ReadAll(resp2.Body)
		var groups []struct {
			ID       int    `json:"id"`
			FullPath string `json:"full_path"`
			Name     string `json:"name"`
		}
		json.Unmarshal(body2, &groups)

		if len(groups) > 0 {
			fmt.Printf("Groups: ")
			groupNames := make([]string, len(groups))
			for i, g := range groups {
				groupNames[i] = g.FullPath
			}
			fmt.Println(strings.Join(groupNames, ", "))

			// Auto-set group_id if not configured and only one group
			if reg.GroupID == 0 {
				if len(groups) == 1 {
					reg.GroupID = groups[0].ID
					reg.Org = groups[0].FullPath
					fmt.Printf("Auto-configured group: %s (id: %d)\n", groups[0].FullPath, groups[0].ID)
				} else {
					fmt.Println("\nMultiple groups found. Set one with:")
					for _, g := range groups {
						fmt.Printf("  gop config set-group --registry %s --group-id %d  # %s\n", reg.Name, g.ID, g.FullPath)
					}
				}
			}
		}
	}

	// 3. Save
	if err := config.Save(cfg, configPath); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("\nRegistry %q is ready. You can now:\n", reg.Name)
	fmt.Printf("  gop search <query>               # search packages\n")
	fmt.Printf("  gop install <name>               # install by name\n")
	if reg.Org != "" {
		fmt.Printf("  gop install %s:%s/repo     # install from group\n", reg.Name, reg.Org)
	}
	return nil
}

func resultHTML(title, message string, success bool) string {
	icon := "&#10004;"
	color := "#2ecc71"
	if !success {
		icon = "&#10008;"
		color = "#e74c3c"
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>gop - %s</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#f0f2f5}
.card{background:white;padding:40px;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,.1);text-align:center;max-width:400px}
.icon{font-size:64px;margin-bottom:16px;color:%s}
h2{color:#1a1a2e;margin:0 0 8px}
p{color:#666;margin:0}
</style></head>
<body><div class="card">
<div class="icon">%s</div>
<h2>%s</h2>
<p>%s</p>
</div></body></html>`, title, color, icon, title, message)
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// configSetOAuthCmd allows users to set OAuth credentials for a registry.
var configSetOAuthCmd = &cobra.Command{
	Use:   "set-oauth",
	Short: "Set OAuth client credentials for a registry",
	Long: `Set OAuth client_id and client_secret for a GitHub or GitLab registry.

For GitHub:
  1. Go to https://github.com/settings/developers
  2. Create OAuth App, enable Device Flow
  3. Run: gop config set-oauth --registry github --client-id APP_ID

For GitLab:
  1. Go to GitLab > Settings > Applications
  2. Create app with redirect URI: http://127.0.0.1:19287/callback
  3. Run: gop config set-oauth --registry mylab --client-id APP_ID --client-secret APP_SECRET

Then: gop login`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		oauthRegName, _ := cmd.Flags().GetString("registry")
		clientID, _ := cmd.Flags().GetString("client-id")
		clientSecret, _ := cmd.Flags().GetString("client-secret")

		if oauthRegName == "" || clientID == "" {
			return fmt.Errorf("--registry and --client-id are required")
		}

		reg, found := mgr.Config.FindRegistry(oauthRegName)
		if !found {
			return fmt.Errorf("registry %q not found", oauthRegName)
		}

		reg.ClientID = clientID
		reg.ClientSecret = clientSecret

		// Also save to global config
		home, err := os.UserHomeDir()
		if err == nil {
			globalPath := filepath.Join(home, config.GlobalConfigDir, config.GlobalConfigFile)
			globalCfg, _ := config.Load(globalPath)
			if globalCfg == nil {
				globalCfg = config.DefaultConfig()
			}
			if gReg, ok := globalCfg.FindRegistry(oauthRegName); ok {
				gReg.ClientID = clientID
				gReg.ClientSecret = clientSecret
			}
			config.Save(globalCfg, globalPath)
		}

		if err := config.Save(mgr.Config, mgr.ConfigPath); err != nil {
			return err
		}

		fmt.Printf("OAuth credentials saved for registry %q.\n", oauthRegName)
		fmt.Println("Now run: gop login")
		return nil
	},
}

func init() {
	loginCmd.Flags().StringVar(&loginRegistry, "registry", "", "registry name to login to")
	loginCmd.Flags().StringVar(&loginMethod, "method", "auto", "login method: auto, oauth, or token")

	configSetOAuthCmd.Flags().String("registry", "", "registry name")
	configSetOAuthCmd.Flags().String("client-id", "", "OAuth client ID")
	configSetOAuthCmd.Flags().String("client-secret", "", "OAuth client secret (GitLab only)")
	configCmd.AddCommand(configSetOAuthCmd)
}
