// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Package projects defines the shared project identity and storage contract.
// Clients resolve a directory locally, then hash its complete UTF-8 real path.
// The server accepts that ID unchanged and never resolves a client's filesystem.
package projects

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"unicode/utf8"
)

// Pattern describes a full lowercase SHA-256 hexadecimal digest.
const Pattern = "^[0-9a-f]{64}$"

var idPattern = regexp.MustCompile(Pattern)

// Validate rejects slugs, directory paths, truncated hashes and uppercase IDs.
func Validate(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid project ID %q: must match %s", id, Pattern)
	}
	return nil
}

// ID hashes an already resolved absolute directory, without case folding,
// Unicode normalization, truncation or character substitution. This pure
// boundary is also used for cross-language vectors; Resolve checks the filesystem.
func ID(directory string) (string, error) {
	if !utf8.ValidString(directory) || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == string(filepath.Separator) {
		return "", fmt.Errorf("project directory must be a resolved absolute directory: %q", directory)
	}
	digest := sha256.Sum256([]byte(directory))
	return hex.EncodeToString(digest[:]), nil
}

// Resolve identifies an existing local directory. Relative inputs are prefixed
// with the absolute working directory without cleaning away symlink/.. segments.
// Evaluating that whole path also resolves a logical PWD supplied by the shell.
// Significant spaces in directory names are never trimmed.
func Resolve(directory string) (string, error) {
	if directory == "" {
		return "", fmt.Errorf("project directory is empty")
	}
	if !filepath.IsAbs(directory) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot resolve working directory: %w", err)
		}
		// filepath.Join and Abs clean parent segments before symlinks are evaluated.
		directory = cwd + string(filepath.Separator) + directory
	}
	real, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("cannot resolve project directory: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("cannot inspect project directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project is not a directory: %s", real)
	}
	return ID(real)
}

// Dir returns one project's storage directory after validating its opaque ID.
func Dir(root, id string) (string, error) {
	if err := Validate(id); err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

// GraphPath returns the JSONL file holding the project's knowledge graph.
func GraphPath(root, id string) (string, error) {
	dir, err := Dir(root, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "memory.jsonl"), nil
}
