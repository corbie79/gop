package registry

import (
	"fmt"

	"github.com/xanzy/go-gitlab"
)

type GitLabRegistry struct {
	client  *gitlab.Client
	baseURL string
	groupID int
}

func NewGitLabRegistry(url, token string, groupID int) (*GitLabRegistry, error) {
	opts := []gitlab.ClientOptionFunc{
		gitlab.WithBaseURL(url),
	}

	client, err := gitlab.NewClient(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GitLab client: %w", err)
	}

	return &GitLabRegistry{
		client:  client,
		baseURL: url,
		groupID: groupID,
	}, nil
}

func (r *GitLabRegistry) Type() string { return "gitlab" }

func (r *GitLabRegistry) Search(query string) ([]PackageResult, error) {
	var results []PackageResult

	opts := &gitlab.ListProjectsOptions{
		Search:  gitlab.Ptr(query),
		OrderBy: gitlab.Ptr("last_activity_at"),
		Sort:    gitlab.Ptr("desc"),
		ListOptions: gitlab.ListOptions{
			PerPage: 20,
			Page:    1,
		},
	}

	var projects []*gitlab.Project
	var err error

	if r.groupID > 0 {
		groupOpts := &gitlab.ListGroupProjectsOptions{
			Search:  gitlab.Ptr(query),
			OrderBy: gitlab.Ptr("last_activity_at"),
			Sort:    gitlab.Ptr("desc"),
			ListOptions: gitlab.ListOptions{
				PerPage: 20,
				Page:    1,
			},
		}
		projects, _, err = r.client.Groups.ListGroupProjects(r.groupID, groupOpts)
	} else {
		projects, _, err = r.client.Projects.ListProjects(opts)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to search projects: %w", err)
	}

	for _, p := range projects {
		result := PackageResult{
			Name:     p.PathWithNamespace,
			URL:      p.WebURL,
			CloneURL: p.HTTPURLToRepo,
			Stars:    p.StarCount,
		}
		if p.Description != "" {
			result.Description = p.Description
		}
		if p.LastActivityAt != nil {
			result.LastUpdate = p.LastActivityAt.Format("2006-01-02")
		}
		results = append(results, result)
	}

	return results, nil
}

func (r *GitLabRegistry) GetProject(path string) (*PackageResult, error) {
	project, _, err := r.client.Projects.GetProject(path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get project %s: %w", path, err)
	}

	result := &PackageResult{
		Name:     project.PathWithNamespace,
		URL:      project.WebURL,
		CloneURL: project.HTTPURLToRepo,
		Stars:    project.StarCount,
	}
	if project.Description != "" {
		result.Description = project.Description
	}
	return result, nil
}

func (r *GitLabRegistry) GetTags(projectPath string) ([]string, error) {
	opts := &gitlab.ListTagsOptions{
		ListOptions: gitlab.ListOptions{
			PerPage: 50,
			Page:    1,
		},
	}

	tags, _, err := r.client.Tags.ListTags(projectPath, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to list tags for %s: %w", projectPath, err)
	}

	var result []string
	for _, t := range tags {
		result = append(result, t.Name)
	}
	return result, nil
}
