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
	"github.com/spf13/cobra"
)

var (
	loginRegistry string
	loginMethod   string
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with GitLab via browser",
	Long: `Open the GitLab web login page and automatically save the token.

Methods:
  oauth   - Full OAuth flow: browser login -> auto token save (default)
  token   - Opens Personal Access Token page, then prompts to paste token

Examples:
  gop login                                    # OAuth with first GitLab registry
  gop login --registry mylab                   # OAuth with specific registry
  gop login --method token --registry mylab    # Manual token via browser`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		var reg *config.Registry
		if loginRegistry != "" {
			r, found := mgr.Config.FindRegistry(loginRegistry)
			if !found {
				return fmt.Errorf("registry %q not found", loginRegistry)
			}
			if r.Type != config.RegistryTypeGitLab {
				return fmt.Errorf("registry %q is not a GitLab registry (type: %s)", loginRegistry, r.Type)
			}
			reg = r
		} else {
			for i := range mgr.Config.Registries {
				if mgr.Config.Registries[i].Type == config.RegistryTypeGitLab {
					reg = &mgr.Config.Registries[i]
					break
				}
			}
			if reg == nil {
				return fmt.Errorf("no GitLab registry configured. Add one first:\n  gop config add-registry --name mylab --type gitlab --url https://gitlab.com")
			}
		}

		baseURL := strings.TrimRight(reg.URL, "/")

		switch loginMethod {
		case "oauth":
			return runOAuthLogin(baseURL, reg, mgr.ConfigPath, mgr.Config)
		case "token":
			return runTokenLogin(baseURL, reg, mgr.ConfigPath, mgr.Config)
		default:
			return fmt.Errorf("unknown method %q (use 'oauth' or 'token')", loginMethod)
		}
	},
}

// runOAuthLogin opens browser, receives callback, exchanges code for token, saves config.
func runOAuthLogin(baseURL string, reg *config.Registry, configPath string, cfg *config.Config) error {
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
		fmt.Println("")

		if err := openBrowser(baseURL + "/-/user_settings/applications"); err != nil {
			fmt.Printf("Open manually: %s/-/user_settings/applications\n", baseURL)
		}
		return nil
	}

	// Start local callback server on fixed port for predictable redirect URI
	listener, err := net.Listen("tcp", "127.0.0.1:19287")
	if err != nil {
		// Try random port as fallback
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("failed to start callback server: %w", err)
		}
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	authURL := fmt.Sprintf("%s/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=read_api+read_repository",
		baseURL,
		url.QueryEscape(reg.ClientID),
		url.QueryEscape(redirectURI))

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	successHTML := `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>gop - Login Successful</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#f0f2f5}
.card{background:white;padding:40px;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,.1);text-align:center;max-width:400px}
.check{font-size:64px;margin-bottom:16px}
h2{color:#1a1a2e;margin:0 0 8px}
p{color:#666;margin:0}
</style></head>
<body><div class="card">
<div class="check">&#10004;</div>
<h2>Login Successful!</h2>
<p>Token has been saved. You can close this window.</p>
</div></body></html>`

	failHTML := `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>gop - Login Failed</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,sans-serif;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#f0f2f5}
.card{background:white;padding:40px;border-radius:12px;box-shadow:0 2px 10px rgba(0,0,0,.1);text-align:center;max-width:400px}
.cross{font-size:64px;margin-bottom:16px;color:#e74c3c}
h2{color:#1a1a2e;margin:0 0 8px}
p{color:#666;margin:0}
</style></head>
<body><div class="card">
<div class="cross">&#10008;</div>
<h2>Login Failed</h2>
<p>%s</p>
</div></body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			errMsg := r.URL.Query().Get("error_description")
			if errMsg == "" {
				errMsg = r.URL.Query().Get("error")
			}
			if errMsg == "" {
				errMsg = "no authorization code received"
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, failHTML, errMsg)
			errCh <- fmt.Errorf("authentication failed: %s", errMsg)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, successHTML)
		codeCh <- code
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	fmt.Println("Opening browser for GitLab login...")
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("\nCannot open browser. Open this URL manually:\n%s\n\n", authURL)
	}
	fmt.Println("Waiting for login in browser...")

	select {
	case code := <-codeCh:
		fmt.Println("Login confirmed! Exchanging token...")

		// Exchange authorization code for access token
		token, err := exchangeCodeForToken(baseURL, reg.ClientID, reg.ClientSecret, code, redirectURI)
		if err != nil {
			return fmt.Errorf("token exchange failed: %w", err)
		}

		// Save token to config
		cfg.UpdateRegistryToken(reg.Name, token)
		if err := config.Save(cfg, configPath); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("\nAuthentication complete! Token saved for registry %q.\n", reg.Name)
		fmt.Println("You can now use: gop install, gop search")
		return nil

	case err := <-errCh:
		return err

	case <-time.After(120 * time.Second):
		return fmt.Errorf("login timed out (2 minutes). Try again with: gop login")
	}
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

func exchangeCodeForToken(baseURL, clientID, clientSecret, code, redirectURI string) (string, error) {
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

	var tokenResp oauthTokenResponse
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

// runTokenLogin opens the PAT page and prompts user to paste the token.
func runTokenLogin(baseURL string, reg *config.Registry, configPath string, cfg *config.Config) error {
	tokenURL := baseURL + "/-/user_settings/personal_access_tokens"

	fmt.Printf("Opening GitLab token page for registry %q...\n\n", reg.Name)
	fmt.Println("Create a token with scopes: read_api, read_repository")

	if err := openBrowser(tokenURL); err != nil {
		fmt.Printf("Open manually: %s\n", tokenURL)
	}

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

	fmt.Printf("\nToken saved for registry %q.\n", reg.Name)
	fmt.Println("You can now use: gop install, gop search")
	return nil
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
	Long: `Set OAuth client_id and client_secret for a GitLab registry.

Steps:
  1. Go to GitLab > Settings > Applications
  2. Create app with redirect URI: http://127.0.0.1:19287/callback
  3. Run this command with the credentials
  4. Run: gop login

Example:
  gop config set-oauth --registry mylab --client-id APP_ID --client-secret APP_SECRET`,
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
	loginCmd.Flags().StringVar(&loginRegistry, "registry", "", "GitLab registry name")
	loginCmd.Flags().StringVar(&loginMethod, "method", "oauth", "login method: oauth or token")

	configSetOAuthCmd.Flags().String("registry", "", "registry name")
	configSetOAuthCmd.Flags().String("client-id", "", "OAuth client ID")
	configSetOAuthCmd.Flags().String("client-secret", "", "OAuth client secret")
	configCmd.AddCommand(configSetOAuthCmd)
}
