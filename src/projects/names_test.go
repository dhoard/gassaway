// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.
package projects_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dhoard/gassaway/src/projects"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	require.NoError(t, projects.Validate(strings.Repeat("a", 64)))
	for _, id := range []string{"", "app", "/work/app", strings.Repeat("a", 63), strings.Repeat("A", 64), strings.Repeat("g", 64), strings.Repeat("a", 65), "../etc"} {
		assert.Error(t, projects.Validate(id), "ID %q must be rejected", id)
	}
}

func TestSharedUTF8Vectors(t *testing.T) {
	data, err := os.ReadFile("testdata/vectors.json")
	require.NoError(t, err)
	var vectors []struct {
		Directory string `json:"directory"`
		ProjectID string `json:"projectId"`
	}
	require.NoError(t, json.Unmarshal(data, &vectors))
	for _, vector := range vectors {
		id, err := projects.ID(vector.Directory)
		require.NoError(t, err)
		assert.Equal(t, vector.ProjectID, id, vector.Directory)
	}
}

func TestResolvePreservesPhysicalDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "a", "app")
	second := filepath.Join(root, "b", "app")
	require.NoError(t, os.MkdirAll(first, 0o755))
	require.NoError(t, os.MkdirAll(second, 0o755))
	a, err := projects.Resolve(first)
	require.NoError(t, err)
	b, err := projects.Resolve(second)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	link := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(first, link))
	alias, err := projects.Resolve(link)
	require.NoError(t, err)
	assert.Equal(t, a, alias)
	t.Chdir(first)
	t.Setenv("PWD", link)
	relative, err := projects.Resolve(".")
	require.NoError(t, err)
	assert.Equal(t, a, relative, "a logical PWD must not change the physical project ID")
	parent, err := projects.Resolve(link + "/..")
	require.NoError(t, err)
	expected, err := projects.Resolve(filepath.Dir(first))
	require.NoError(t, err)
	assert.Equal(t, expected, parent)
	relativeParent, err := projects.Resolve("..")
	require.NoError(t, err)
	assert.Equal(t, expected, relativeParent)
}

func TestResolveRequiresExistingDirectory(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, []byte("content"), 0o600))
	for _, value := range []string{file, filepath.Join(root, "missing"), ""} {
		_, err := projects.Resolve(value)
		require.Error(t, err)
	}
	for _, value := range []string{"relative", "/a/../b", "/a/", "/"} {
		_, err := projects.ID(value)
		require.Error(t, err)
	}
}

func TestGraphPathRequiresID(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("a", 64)
	path, err := projects.GraphPath(root, id)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, id, "memory.jsonl"), path)
	for _, value := range []string{"..", "../etc", "a/b", "app", "/work/app"} {
		_, err := projects.GraphPath(root, value)
		require.Error(t, err)
	}
}
