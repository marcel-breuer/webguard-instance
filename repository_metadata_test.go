package repository_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryMetadataUsesCurrentProjectIdentity(t *testing.T) {
	oldOwner := "m-" + "breuer"
	oldProject := "webguard-instance-" + "v2"
	forbidden := []string{
		"github.com/" + oldOwner + "/",
		"ghcr.io/" + oldOwner + "/",
		oldProject,
		"New " + "Version",
		"new " + "version of the WebGuard instance service",
	}

	err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		for _, marker := range forbidden {
			if strings.Contains(content, marker) {
				t.Errorf("%s still contains obsolete project identity marker %q", path, marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
