package autonomy

import (
	"os"
	"path/filepath"
)

// InspectCompletionTree uses the same path, file and digest rules as overlay
// reconstruction. The caller must freeze the tree before inspecting it.
func InspectCompletionTree(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := completionNoLinkedAncestors(abs); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return "", err
	}
	defer root.Close()
	entries, err := completionScan(root)
	if err != nil {
		return "", err
	}
	return completionTreeHash(entries), nil
}
