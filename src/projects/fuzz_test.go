// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

package projects_test

import (
	"path/filepath"
	"testing"

	"github.com/dhoard/gassaway/src/projects"
)

// FuzzValidate is the project-ID gate: it must accept exactly a 64-character
// lowercase hexadecimal digest and nothing else, and a refused ID must never
// yield a path. Accepting anything else is a path-traversal or a
// distinct-name-for-one-project bug, so the property is checked rather than the
// examples.
func FuzzValidate(f *testing.F) {
	for _, seed := range []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"",
		"app",
		"../etc",
		strings64('A'),
		strings64('g'),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, id string) {
		err := projects.Validate(id)
		if err == nil {
			if len(id) != 64 {
				t.Fatalf("accepted an ID of length %d: %q", len(id), id)
			}
			for i := 0; i < len(id); i++ {
				if !(id[i] >= '0' && id[i] <= '9' || id[i] >= 'a' && id[i] <= 'f') {
					t.Fatalf("accepted %q: byte %d is %q", id, i, id[i])
				}
			}
		}

		dir, dirErr := projects.Dir("/srv/gassaway", id)
		if err == nil && dirErr != nil {
			t.Fatalf("a valid ID %q must yield a directory: %v", id, dirErr)
		}
		if err != nil && dirErr == nil {
			t.Fatalf("a refused ID %q must not yield a directory (%q)", id, dir)
		}
		if dirErr == nil && filepath.Dir(dir) != "/srv/gassaway" {
			t.Fatalf("ID %q escaped the root: %q", id, dir)
		}
	})
}

// FuzzID checks the hashing boundary: a resolved absolute path hashes to 64
// lowercase hex characters, and anything else is refused rather than hashed into
// a plausible-looking identity.
func FuzzID(f *testing.F) {
	for _, seed := range []string{"/srv/app", "/", "", "relative/app", "/srv/../app", "/srv/app/", "/srv/\x00app"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, directory string) {
		id, err := projects.ID(directory)
		if err != nil {
			return
		}
		if err := projects.Validate(id); err != nil {
			t.Fatalf("ID(%q) produced an invalid project ID %q: %v", directory, id, err)
		}
	})
}

func strings64(b byte) string {
	out := make([]byte, 64)
	for i := range out {
		out[i] = b
	}
	return string(out)
}
