//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// updatePath adds the given directory to the user's shell configuration files
// (e.g. ~/.bashrc, ~/.zshrc, ~/.profile) on Unix-like systems.
func updatePath(installDir string) (bool, error) {
	// First, check if the installDir is already present in the active PATH env var
	pathEnv := os.Getenv("PATH")
	normalizedInstallDir := filepathClean(installDir)
	for _, p := range filepath.SplitList(pathEnv) {
		if filepathClean(p) == normalizedInstallDir {
			return false, nil // Already in PATH
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false, fmt.Errorf("could not find user home directory: %w", err)
	}

	// Determine shell profiles to check/modify
	var profilesToModify []string
	shell := os.Getenv("SHELL")
	if strings.Contains(shell, "zsh") {
		profilesToModify = append(profilesToModify, filepath.Join(home, ".zshrc"))
	} else if strings.Contains(shell, "bash") {
		profilesToModify = append(profilesToModify, filepath.Join(home, ".bashrc"))
	} else {
		// Fallback/standard profiles
		profilesToModify = append(profilesToModify, filepath.Join(home, ".profile"))
		// If standard shell config files exist, modify them too
		if _, err := os.Stat(filepath.Join(home, ".bashrc")); err == nil {
			profilesToModify = append(profilesToModify, filepath.Join(home, ".bashrc"))
		}
		if _, err := os.Stat(filepath.Join(home, ".zshrc")); err == nil {
			profilesToModify = append(profilesToModify, filepath.Join(home, ".zshrc"))
		}
	}

	exportLine := fmt.Sprintf("\n# FusionDB bin PATH\nexport PATH=\"$PATH:%s\"\n", installDir)
	modifiedAny := false

	for _, profilePath := range profilesToModify {
		content, err := os.ReadFile(profilePath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				continue // skip files we can't read
			}
			// If it's a fallback file and doesn't exist, we don't want to create it
			// unless it's ~/.profile
			if filepath.Base(profilePath) != ".profile" {
				continue
			}
			content = []byte{}
		}

		// Check if already contains the installDir path
		if strings.Contains(string(content), installDir) {
			continue
		}

		// Open in append mode (create if it's ~/.profile and doesn't exist)
		f, err := os.OpenFile(profilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return false, fmt.Errorf("failed to open profile file %s: %w", profilePath, err)
		}
		_, err = f.WriteString(exportLine)
		f.Close()
		if err != nil {
			return false, fmt.Errorf("failed to write to profile file %s: %w", profilePath, err)
		}
		modifiedAny = true
	}

	return modifiedAny, nil
}

// filepathClean cleans a filepath string for comparison.
func filepathClean(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ToLower(p)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSuffix(p, "/")
	return p
}
