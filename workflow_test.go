package webguardinstance_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerImageWorkflowGeneratesChangelogOnTag(t *testing.T) {
	workflowPath := filepath.Join(".github", "workflows", "docker-image.yml")
	content, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read docker image workflow: %v", err)
	}

	workflow := string(content)
	requiredSnippets := []string{
		`tags:
      - "v*"`,
		"release:",
		"name: Generate Changelog",
		"needs: publish",
		"if: startsWith(github.ref, 'refs/tags/')",
		"contents: write",
		`gh release view "$GITHUB_REF_NAME"`,
		`gh release create "$GITHUB_REF_NAME" \`,
		"--verify-tag",
		"--generate-notes",
	}

	for _, snippet := range requiredSnippets {
		if !strings.Contains(workflow, snippet) {
			t.Fatalf("docker image workflow should contain %q", snippet)
		}
	}
}
