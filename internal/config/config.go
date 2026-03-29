package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigFile  = "gop.yaml"
	DefaultLockFile    = "gop-lock.yaml"
	DefaultInstallDir  = ".gop_packages"
	GlobalConfigDir    = ".gop"
	GlobalConfigFile   = "config.yaml"
	RegistryTypeGit    = "git"
	RegistryTypeGitLab = "gitlab"
)

type Config struct {
	Registries []Registry `yaml:"registries,omitempty"`
	Packages   []Package  `yaml:"packages,omitempty"`
	InstallDir string     `yaml:"install_dir,omitempty"`
}

type Registry struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	URL     string `yaml:"url"`
	Token   string `yaml:"token,omitempty"`
	GroupID int    `yaml:"group_id,omitempty"`
}

type Package struct {
	Name    string `yaml:"name"`
	Source  string `yaml:"source"`
	Version string `yaml:"version,omitempty"`
}

var envVarRegex = regexp.MustCompile(`\$\{([^}]+)\}`)

func expandEnvVars(s string) string {
	return envVarRegex.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarRegex.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match
	})
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}

	// Expand env vars in registry tokens
	for i := range cfg.Registries {
		cfg.Registries[i].Token = expandEnvVars(cfg.Registries[i].Token)
	}

	if cfg.InstallDir == "" {
		cfg.InstallDir = DefaultInstallDir
	}

	return &cfg, nil
}

func Save(cfg *Config, path string) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", path, err)
	}
	return nil
}

func LoadGlobal() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, GlobalConfigDir, GlobalConfigFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Config{}, nil
	}
	return Load(path)
}

func (c *Config) Merge(other *Config) {
	// Merge registries (local overrides global by name)
	existing := make(map[string]bool)
	for _, r := range c.Registries {
		existing[r.Name] = true
	}
	for _, r := range other.Registries {
		if !existing[r.Name] {
			c.Registries = append(c.Registries, r)
		}
	}
}

func (c *Config) FindRegistry(name string) (*Registry, bool) {
	for i := range c.Registries {
		if c.Registries[i].Name == name {
			return &c.Registries[i], true
		}
	}
	return nil, false
}

func (c *Config) AddPackage(pkg Package) {
	for i, p := range c.Packages {
		if p.Name == pkg.Name {
			c.Packages[i] = pkg
			return
		}
	}
	c.Packages = append(c.Packages, pkg)
}

func (c *Config) RemovePackage(name string) bool {
	for i, p := range c.Packages {
		if p.Name == name {
			c.Packages = append(c.Packages[:i], c.Packages[i+1:]...)
			return true
		}
	}
	return false
}

func (c *Config) FindPackage(name string) (*Package, bool) {
	for i := range c.Packages {
		if c.Packages[i].Name == name {
			return &c.Packages[i], true
		}
	}
	return nil, false
}

// ResolveSource resolves a package source to a full git URL.
// If the source contains ":" and is not a full URL, it's treated as "registry:path".
func (c *Config) ResolveSource(source string) (string, string, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "git@") {
		return source, "", nil
	}

	parts := strings.SplitN(source, ":", 2)
	if len(parts) == 2 {
		reg, found := c.FindRegistry(parts[0])
		if !found {
			return "", "", fmt.Errorf("registry %q not found", parts[0])
		}
		url := strings.TrimRight(reg.URL, "/") + "/" + parts[1]
		if !strings.HasSuffix(url, ".git") {
			url += ".git"
		}
		return url, reg.Token, nil
	}

	return source, "", nil
}

func DefaultConfig() *Config {
	return &Config{
		InstallDir: DefaultInstallDir,
		Registries: []Registry{},
		Packages:   []Package{},
	}
}
