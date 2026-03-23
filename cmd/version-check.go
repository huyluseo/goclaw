package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	githubReleasesURL = "https://api.github.com/repos/nextlevelbuilder/goclaw/releases/latest"
	checkTimeout      = 5 * time.Second
)

// githubRelease holds the tag_name from the GitHub releases API response.
type githubRelease struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

// checkLatestVersion fetches the latest release tag from GitHub.
// Returns empty strings if the check fails (no network, rate-limited, etc.).
func checkLatestVersion() (tag string, url string) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubReleasesURL, nil)
	if err != nil {
		return "", ""
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", ""
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", ""
	}
	return rel.TagName, rel.HTMLURL
}

// normalizeVersion strips the "v" prefix and any "-dirty"/"-<hash>" build suffix
// so that "v2.4.7-dirty" becomes "2.4.7".
func normalizeVersion(v string) string {
	v = strings.TrimPrefix(v, "v")
	// Strip build metadata after first hyphen (e.g. "2.4.7-dirty" -> "2.4.7")
	if idx := strings.IndexByte(v, '-'); idx != -1 {
		v = v[:idx]
	}
	return v
}

// printVersionCheck prints update availability information.
func printVersionCheck(current string) {
	latest, url := checkLatestVersion()
	if latest == "" {
		return
	}

	currentNorm := normalizeVersion(current)
	latestNorm := normalizeVersion(latest)

	if currentNorm == latestNorm || current == "dev" {
		fmt.Println("Up to date.")
		return
	}

	if compareVersions(currentNorm, latestNorm) < 0 {
		fmt.Printf("\nUpdate available: %s -> %s\n", current, latest)
		fmt.Printf("Release: %s\n", url)
	}
}

// compareVersions compares two dot-separated version strings.
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func compareVersions(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}

	for i := 0; i < maxLen; i++ {
		var na, nb int
		if i < len(partsA) {
			fmt.Sscanf(partsA[i], "%d", &na)
		}
		if i < len(partsB) {
			fmt.Sscanf(partsB[i], "%d", &nb)
		}
		if na < nb {
			return -1
		}
		if na > nb {
			return 1
		}
	}
	return 0
}
