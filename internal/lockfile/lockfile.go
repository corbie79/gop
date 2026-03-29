package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type LockFile struct {
	Packages []LockedPackage `yaml:"packages,omitempty"`
}

type LockedPackage struct {
	Name           string    `yaml:"name"`
	Source         string    `yaml:"source"`
	Version        string    `yaml:"version,omitempty"`
	ResolvedCommit string    `yaml:"resolved_commit"`
	InstalledAt    time.Time `yaml:"installed_at"`
}

func Load(path string) (*LockFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &LockFile{}, nil
		}
		return nil, fmt.Errorf("failed to read lock file %s: %w", path, err)
	}

	var lf LockFile
	if err := yaml.Unmarshal(data, &lf); err != nil {
		return nil, fmt.Errorf("failed to parse lock file %s: %w", path, err)
	}
	return &lf, nil
}

func (l *LockFile) Save(path string) error {
	data, err := yaml.Marshal(l)
	if err != nil {
		return fmt.Errorf("failed to marshal lock file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write lock file %s: %w", path, err)
	}
	return nil
}

func (l *LockFile) Get(name string) (*LockedPackage, bool) {
	for i := range l.Packages {
		if l.Packages[i].Name == name {
			return &l.Packages[i], true
		}
	}
	return nil, false
}

func (l *LockFile) Set(pkg LockedPackage) {
	for i, p := range l.Packages {
		if p.Name == pkg.Name {
			l.Packages[i] = pkg
			return
		}
	}
	l.Packages = append(l.Packages, pkg)
}

func (l *LockFile) Remove(name string) bool {
	for i, p := range l.Packages {
		if p.Name == name {
			l.Packages = append(l.Packages[:i], l.Packages[i+1:]...)
			return true
		}
	}
	return false
}
