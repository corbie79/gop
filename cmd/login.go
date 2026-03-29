package cmd

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
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
	Short: "Open GitLab web login page in browser",
	Long: `Open the GitLab web login page to authenticate.

Methods:
  token   - Opens the Personal Access Token creation page (default)
  oauth   - Opens OAuth authorization flow with local callback server

Examples:
  gop login --registry mylab
  gop login --registry mylab --method oauth
  gop login --registry mylab --method token`,
	RunE: func(cmd *cobra.Command, args []string) error {
		mgr, err := loadManager()
		if err != nil {
			return err
		}

		// Find registry
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
			// Find first GitLab registry
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
		case "token":
			return openTokenPage(baseURL, reg.Name)
		case "oauth":
			return openOAuthFlow(baseURL, reg.Name)
		default:
			return fmt.Errorf("unknown method %q (use 'token' or 'oauth')", loginMethod)
		}
	},
}

func openTokenPage(baseURL, regName string) error {
	// GitLab Personal Access Token page
	tokenURL := baseURL + "/-/user_settings/personal_access_tokens"

	fmt.Printf("Opening GitLab Personal Access Token page for registry %q...\n", regName)
	fmt.Printf("URL: %s\n\n", tokenURL)
	fmt.Println("Create a token with the following scopes:")
	fmt.Println("  - read_api (for searching projects)")
	fmt.Println("  - read_repository (for cloning)")
	fmt.Println("")
	fmt.Println("After creating the token, save it with:")
	fmt.Printf("  gop config add-registry --name %s --type gitlab --url %s --token YOUR_TOKEN\n", regName, baseURL)
	fmt.Println("")
	fmt.Println("Or set it as an environment variable:")
	fmt.Printf("  export GITLAB_TOKEN=YOUR_TOKEN\n")

	if err := openBrowser(tokenURL); err != nil {
		fmt.Printf("\nCould not open browser automatically. Please open the URL manually:\n%s\n", tokenURL)
	}

	return nil
}

func openOAuthFlow(baseURL, regName string) error {
	// Start local callback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to start callback server: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	// GitLab OAuth authorization URL
	// Users need to register an OAuth app in GitLab first
	authURL := fmt.Sprintf("%s/oauth/authorize?client_id=GOP_CLIENT_ID&redirect_uri=%s&response_type=code&scope=read_api+read_repository",
		baseURL, callbackURL)

	fmt.Printf("Opening GitLab OAuth login for registry %q...\n", regName)
	fmt.Printf("Callback server listening on: %s\n\n", callbackURL)
	fmt.Println("NOTE: For OAuth to work, you need to register an OAuth application in GitLab:")
	fmt.Printf("  %s/-/user_settings/applications\n\n", baseURL)
	fmt.Println("Set the redirect URI to:", callbackURL)
	fmt.Println("")

	tokenCh := make(chan string, 1)
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
			fmt.Fprintf(w, "<html><body><h2>Authentication Failed</h2><p>%s</p><p>You can close this window.</p></body></html>", errMsg)
			errCh <- fmt.Errorf("OAuth failed: %s", errMsg)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><body><h2>Authentication Successful!</h2><p>Authorization code received. You can close this window.</p></body></html>")
		tokenCh <- code
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)

	if err := openBrowser(authURL); err != nil {
		fmt.Printf("Could not open browser automatically. Please open:\n%s\n\n", authURL)
	}

	fmt.Println("Waiting for authentication (30s timeout)...")

	select {
	case code := <-tokenCh:
		server.Close()
		fmt.Printf("\nAuthorization code received: %s...\n", code[:min(len(code), 10)])
		fmt.Println("\nTo complete OAuth flow, exchange this code for an access token.")
		fmt.Println("Then save it with:")
		fmt.Printf("  gop config add-registry --name %s --type gitlab --url %s --token ACCESS_TOKEN\n", regName, baseURL)
		return nil
	case err := <-errCh:
		server.Close()
		return err
	case <-time.After(30 * time.Second):
		server.Close()
		return fmt.Errorf("authentication timed out after 30 seconds")
	}
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

func init() {
	loginCmd.Flags().StringVar(&loginRegistry, "registry", "", "GitLab registry name")
	loginCmd.Flags().StringVar(&loginMethod, "method", "token", "login method: token or oauth")
}
