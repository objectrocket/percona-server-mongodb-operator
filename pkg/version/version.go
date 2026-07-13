package version

import (
	_ "embed"
	"strings"
)

//go:generate sh -c "yq -i '.metadata.labels.\"app.kubernetes.io/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_psmdb.yaml"
//go:generate sh -c "yq -i '.metadata.labels.\"app.kubernetes.io/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_psmdbbackup.yaml"
//go:generate sh -c "yq -i '.metadata.labels.\"app.kubernetes.io/version\" = \"v\" + load(\"version.txt\")' ../../config/crd/patches/versionlabel_in_psmdbrestore.yaml"

//go:embed version.txt
var version string

// Version returns the base semver version (X.Y.Z) used for CRVersion
// feature-gating comparisons. The subversion suffix (-N) is stripped
// because hashicorp/go-version treats hyphens as prerelease identifiers,
// which would break version comparison logic.
func Version() string {
	v := strings.TrimSpace(version)
	// Strip the subversion suffix (-N) if present.
	// The full version including subversion is in FullVersion().
	if idx := strings.LastIndex(v, "-"); idx > 0 {
		// Only strip if the part after the last hyphen is numeric (our subversion).
		suffix := v[idx+1:]
		isNumeric := true
		for _, c := range suffix {
			if c < '0' || c > '9' {
				isNumeric = false
				break
			}
		}
		if isNumeric {
			return v[:idx]
		}
	}
	return v
}

// FullVersion returns the complete version string including the subversion
// suffix (e.g., "1.22.0-1"). Used for image tagging and display purposes.
func FullVersion() string {
	return strings.TrimSpace(version)
}
