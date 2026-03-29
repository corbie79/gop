package gitclient

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
)

type GitClient struct {
	Token string
}

type CloneResult struct {
	CommitHash string
	Reference  string
}

func New(token string) *GitClient {
	return &GitClient{Token: token}
}

func (g *GitClient) authMethod() *http.BasicAuth {
	if g.Token == "" {
		return nil
	}
	return &http.BasicAuth{
		Username: "gop",
		Password: g.Token,
	}
}

func (g *GitClient) Clone(url, dest, version string) (*CloneResult, error) {
	opts := &git.CloneOptions{
		URL:      url,
		Progress: os.Stdout,
	}

	auth := g.authMethod()
	if auth != nil {
		opts.Auth = auth
	}

	repo, err := git.PlainClone(dest, false, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to clone %s: %w", url, err)
	}

	if version != "" {
		result, err := g.checkoutVersion(repo, version)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}
	return &CloneResult{
		CommitHash: head.Hash().String(),
		Reference:  head.Name().Short(),
	}, nil
}

func (g *GitClient) checkoutVersion(repo *git.Repository, version string) (*CloneResult, error) {
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}

	// Try as tag first
	tagRef, err := repo.Tag(version)
	if err == nil {
		if err := wt.Checkout(&git.CheckoutOptions{Hash: tagRef.Hash()}); err != nil {
			// Tag might be annotated, resolve it
			tagObj, err2 := repo.TagObject(tagRef.Hash())
			if err2 == nil {
				commit, err3 := tagObj.Commit()
				if err3 == nil {
					if err := wt.Checkout(&git.CheckoutOptions{Hash: commit.Hash}); err != nil {
						return nil, fmt.Errorf("failed to checkout tag %s: %w", version, err)
					}
					return &CloneResult{CommitHash: commit.Hash.String(), Reference: version}, nil
				}
			}
			return nil, fmt.Errorf("failed to checkout tag %s: %w", version, err)
		}
		return &CloneResult{CommitHash: tagRef.Hash().String(), Reference: version}, nil
	}

	// Try as remote branch
	branchRef := plumbing.NewRemoteReferenceName("origin", version)
	ref, err := repo.Reference(branchRef, true)
	if err == nil {
		if err := wt.Checkout(&git.CheckoutOptions{
			Hash:   ref.Hash(),
			Branch: plumbing.NewBranchReferenceName(version),
			Create: true,
		}); err != nil {
			return nil, fmt.Errorf("failed to checkout branch %s: %w", version, err)
		}
		return &CloneResult{CommitHash: ref.Hash().String(), Reference: version}, nil
	}

	// Try as commit hash
	if len(version) >= 7 {
		hash := plumbing.NewHash(version)
		if err := wt.Checkout(&git.CheckoutOptions{Hash: hash}); err != nil {
			return nil, fmt.Errorf("failed to checkout commit %s: %w", version, err)
		}
		return &CloneResult{CommitHash: hash.String(), Reference: version}, nil
	}

	return nil, fmt.Errorf("could not resolve version %q: not a valid tag, branch, or commit hash", version)
}

func (g *GitClient) Pull(repoPath string) (*CloneResult, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository at %s: %w", repoPath, err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}

	opts := &git.PullOptions{
		RemoteName: "origin",
	}
	auth := g.authMethod()
	if auth != nil {
		opts.Auth = auth
	}

	err = wt.Pull(opts)
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return nil, fmt.Errorf("failed to pull: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}
	return &CloneResult{
		CommitHash: head.Hash().String(),
		Reference:  head.Name().Short(),
	}, nil
}

func (g *GitClient) Fetch(repoPath string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("failed to open repository at %s: %w", repoPath, err)
	}

	opts := &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{"+refs/*:refs/*"},
		Force:      true,
	}
	auth := g.authMethod()
	if auth != nil {
		opts.Auth = auth
	}

	err = repo.Fetch(opts)
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("failed to fetch: %w", err)
	}
	return nil
}

func (g *GitClient) CheckoutVersion(repoPath, version string) (*CloneResult, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository at %s: %w", repoPath, err)
	}
	return g.checkoutVersion(repo, version)
}

func (g *GitClient) ListTags(repoPath string) ([]string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	tagRefs, err := repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("failed to list tags: %w", err)
	}

	var tags []string
	tagRefs.ForEach(func(ref *plumbing.Reference) error {
		tags = append(tags, ref.Name().Short())
		return nil
	})
	return tags, nil
}

// ExtractRepoName extracts a package name from a git URL.
func ExtractRepoName(url string) string {
	// Remove trailing .git
	url = strings.TrimSuffix(url, ".git")
	// Get last path component
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		name := parts[len(parts)-1]
		// Handle SSH URLs like git@github.com:org/repo
		if idx := strings.LastIndex(name, ":"); idx >= 0 {
			name = name[idx+1:]
		}
		return name
	}
	return url
}
