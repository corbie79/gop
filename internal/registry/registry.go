package registry

// Registry is the common interface for all registry types (GitHub, GitLab, generic Git).
type Registry interface {
	// Type returns the registry type identifier.
	Type() string

	// Search searches for projects/repos matching the query.
	Search(query string) ([]PackageResult, error)

	// GetProject returns details for a specific project.
	GetProject(path string) (*PackageResult, error)

	// GetTags returns available tags for a project.
	GetTags(projectPath string) ([]string, error)
}

type PackageResult struct {
	Name        string
	Description string
	URL         string
	CloneURL    string
	Stars       int
	LastUpdate  string
	Registry    string // registry name that produced this result
}
