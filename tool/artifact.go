package tool

import (
	"os"
	"path/filepath"
	"strings"
)

// ArtifactRoot returns the platform-appropriate artifact directory.
// Linux/macOS: /tmp/openagent
// Windows:     %TEMP%\openagent
//
// Tool results exceeding a size threshold can be saved here by hooks and
// referenced in the tool result summary. The system tmp cleaner reclaims
// the space eventually, so artifacts are best-effort persistent.
func ArtifactRoot() string {
	return filepath.Join(os.TempDir(), "openagent")
}

// isWithinArtifactDir reports whether resolved (absolute, symlink-resolved)
// is within ArtifactRoot. Tools use this in CanSelfApprove to allow
// read/write access to artifact files without user approval.
func isWithinArtifactDir(resolved string) bool {
	root := ArtifactRoot()
	return resolved == root || strings.HasPrefix(resolved, root+string(os.PathSeparator))
}
