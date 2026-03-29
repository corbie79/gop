package registry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type GitHubRegistry struct {
	token   string
	baseURL string // https://api.github.com or GitHub Enterprise API URL
	org     string // optional: restrict search to org/user
}

func NewGitHubRegistry(apiURL, token, org string) *GitHubRegistry {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	apiURL = strings.TrimRight(apiURL, "/")
	// Convert web URL to API URL for github.com
	if apiURL == "https://github.com" {
		apiURL = "https://api.github.com"
	}
	return &GitHubRegistry{
		token:   token,
		baseURL: apiURL,
		org:     org,
	}
}

func (g *GitHubRegistry) Type() string { return "github" }

func (g *GitHubRegistry) doRequest(path string) ([]byte, error) {
	req, err := http.NewRequest("GET", g.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

type ghSearchResponse struct {
	Items []ghRepo `json:"items"`
}

type ghRepo struct {
	FullName    string    `json:"full_name"`
	Description string    `json:"description"`
	HTMLURL     string    `json:"html_url"`
	CloneURL    string    `json:"clone_url"`
	Stars       int       `json:"stargazers_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (g *GitHubRegistry) Search(query string) ([]PackageResult, error) {
	q := query
	if g.org != "" {
		q = fmt.Sprintf("%s org:%s", query, g.org)
	}

	path := fmt.Sprintf("/search/repositories?q=%s&sort=updated&per_page=20", url.QueryEscape(q))
	body, err := g.doRequest(path)
	if err != nil {
		return nil, err
	}

	var searchResp ghSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to parse search response: %w", err)
	}

	var results []PackageResult
	for _, repo := range searchResp.Items {
		results = append(results, PackageResult{
			Name:        repo.FullName,
			Description: repo.Description,
			URL:         repo.HTMLURL,
			CloneURL:    repo.CloneURL,
			Stars:       repo.Stars,
			LastUpdate:  repo.UpdatedAt.Format("2006-01-02"),
		})
	}
	return results, nil
}

func (g *GitHubRegistry) GetProject(path string) (*PackageResult, error) {
	body, err := g.doRequest("/repos/" + path)
	if err != nil {
		return nil, err
	}

	var repo ghRepo
	if err := json.Unmarshal(body, &repo); err != nil {
		return nil, fmt.Errorf("failed to parse repo response: %w", err)
	}

	return &PackageResult{
		Name:        repo.FullName,
		Description: repo.Description,
		URL:         repo.HTMLURL,
		CloneURL:    repo.CloneURL,
		Stars:       repo.Stars,
		LastUpdate:  repo.UpdatedAt.Format("2006-01-02"),
	}, nil
}

type ghTag struct {
	Name string `json:"name"`
}

func (g *GitHubRegistry) GetTags(projectPath string) ([]string, error) {
	body, err := g.doRequest(fmt.Sprintf("/repos/%s/tags?per_page=50", projectPath))
	if err != nil {
		return nil, err
	}

	var tags []ghTag
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, fmt.Errorf("failed to parse tags response: %w", err)
	}

	var result []string
	for _, t := range tags {
		result = append(result, t.Name)
	}
	return result, nil
}

// === GitHub Device Flow (for CLI login) ===

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type DeviceTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// RequestDeviceCode initiates the GitHub Device Flow.
func RequestDeviceCode(clientID string) (*DeviceCodeResponse, error) {
	data := url.Values{
		"client_id": {clientID},
		"scope":     {"repo read:org"},
	}

	req, err := http.NewRequest("POST", "https://github.com/login/device/code", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request device code: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var dcResp DeviceCodeResponse
	if err := json.Unmarshal(body, &dcResp); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	if dcResp.DeviceCode == "" {
		return nil, fmt.Errorf("empty device code, response: %s", string(body))
	}

	return &dcResp, nil
}

// PollForToken polls GitHub until the user authorizes the device.
func PollForToken(clientID, deviceCode string, interval int) (*DeviceTokenResponse, error) {
	if interval < 5 {
		interval = 5
	}

	for {
		time.Sleep(time.Duration(interval) * time.Second)

		data := url.Values{
			"client_id":   {clientID},
			"device_code": {deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}

		req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			continue // retry on network error
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			continue
		}

		var tokenResp DeviceTokenResponse
		if err := json.Unmarshal(body, &tokenResp); err != nil {
			continue
		}

		switch tokenResp.Error {
		case "":
			if tokenResp.AccessToken != "" {
				return &tokenResp, nil
			}
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5
			continue
		case "expired_token":
			return nil, fmt.Errorf("device code expired, please try again")
		case "access_denied":
			return nil, fmt.Errorf("login denied by user")
		default:
			return nil, fmt.Errorf("OAuth error: %s - %s", tokenResp.Error, tokenResp.ErrorDesc)
		}
	}
}
