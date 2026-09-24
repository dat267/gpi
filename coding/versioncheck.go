package coding

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Port of utils/version-check.ts, including the semver `valid`/`compare`
// subset the release check needs (the Go port has no semver dependency).

// PortReleaseURL is where this module's own releases are published: the Go
// module proxy, which is what `go install github.com/dat267/pier@latest`
// resolves. Upstream asks pi's release feed (utils/version-check.ts), whose
// versions are pi's, not this port's.
const PortReleaseURL = "https://proxy.golang.org/github.com/dat267/pier/@latest"

// DefaultVersionCheckTimeoutMS bounds the version request.
const DefaultVersionCheckTimeoutMS int64 = 10_000

// LatestRelease is the published release info. The module proxy carries only a
// version (no changelog note, unlike upstream's feed).
type LatestRelease struct {
	Version string
}

// ComparePackageVersions compares two semver strings; ok is false when either
// side is not valid semver.
func ComparePackageVersions(leftVersion, rightVersion string) (int, bool) {
	left, ok := parseSemver(strings.TrimSpace(leftVersion))
	if !ok {
		return 0, false
	}
	right, ok := parseSemver(strings.TrimSpace(rightVersion))
	if !ok {
		return 0, false
	}
	return left.compare(right), true
}

// IsNewerPackageVersion reports whether candidate is newer than current. When
// either side is not valid semver the raw strings are compared instead.
func IsNewerPackageVersion(candidateVersion, currentVersion string) bool {
	if comparison, ok := ComparePackageVersions(candidateVersion, currentVersion); ok {
		return comparison > 0
	}
	return strings.TrimSpace(candidateVersion) != strings.TrimSpace(currentVersion)
}

// semver is a parsed semantic version.
type semver struct {
	major, minor, patch int
	prerelease          []string
	build               string
}

// parseSemver parses a semver string (the npm `semver.valid` subset: strict
// x.y.z with optional -prerelease and +build).
func parseSemver(value string) (semver, bool) {
	if value == "" {
		return semver{}, false
	}
	main := value
	// npm's semver accepts one leading "v" and strips it (`valid("v1.2.3")` is
	// "1.2.3"), which matters because Go module versions keep the tag's prefix.
	if strings.HasPrefix(main, "v") {
		main = main[1:]
	}
	if index := strings.Index(main, "+"); index >= 0 {
		build := main[index+1:]
		if build == "" || !validBuildIdentifiers(build) {
			return semver{}, false
		}
		main = main[:index]
	}
	var prerelease []string
	if index := strings.Index(main, "-"); index >= 0 {
		pre := main[index+1:]
		if pre == "" || !validPrereleaseIdentifiers(pre) {
			return semver{}, false
		}
		prerelease = strings.Split(pre, ".")
		main = main[:index]
	}
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	numbers := make([]int, 3)
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return semver{}, false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return semver{}, false
		}
		numbers[index] = number
	}
	return semver{major: numbers[0], minor: numbers[1], patch: numbers[2], prerelease: prerelease}, true
}

func validPrereleaseIdentifiers(value string) bool {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, character := range identifier {
			if character < '0' || character > '9' {
				numeric = false
				break
			}
		}
		if numeric {
			// Numeric identifiers must not have leading zeroes.
			if len(identifier) > 1 && identifier[0] == '0' {
				return false
			}
			continue
		}
		for _, character := range identifier {
			if !isSemverIdentifierCharacter(character) {
				return false
			}
		}
	}
	return true
}

func validBuildIdentifiers(value string) bool {
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		for _, character := range identifier {
			if !isSemverIdentifierCharacter(character) {
				return false
			}
		}
	}
	return true
}

func isSemverIdentifierCharacter(character rune) bool {
	switch {
	case character >= '0' && character <= '9':
		return true
	case character >= 'a' && character <= 'z':
		return true
	case character >= 'A' && character <= 'Z':
		return true
	case character == '-':
		return true
	}
	return false
}

// compare returns -1, 0, or 1 per semver precedence.
func (v semver) compare(other semver) int {
	if v.major != other.major {
		return compareInts(v.major, other.major)
	}
	if v.minor != other.minor {
		return compareInts(v.minor, other.minor)
	}
	if v.patch != other.patch {
		return compareInts(v.patch, other.patch)
	}
	// A version without a prerelease outranks one with a prerelease.
	if len(v.prerelease) == 0 && len(other.prerelease) == 0 {
		return 0
	}
	if len(v.prerelease) == 0 {
		return 1
	}
	if len(other.prerelease) == 0 {
		return -1
	}
	length := len(v.prerelease)
	if len(other.prerelease) < length {
		length = len(other.prerelease)
	}
	for index := 0; index < length; index++ {
		left, right := v.prerelease[index], other.prerelease[index]
		leftNumber, leftErr := strconv.Atoi(left)
		rightNumber, rightErr := strconv.Atoi(right)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber != rightNumber {
				return compareInts(leftNumber, rightNumber)
			}
		case leftErr == nil:
			return -1 // numeric identifiers rank lower than alphanumeric
		case rightErr == nil:
			return 1
		default:
			if left != right {
				if left < right {
					return -1
				}
				return 1
			}
		}
	}
	return compareInts(len(v.prerelease), len(other.prerelease))
}

func compareInts(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

// GetLatestPortRelease fetches this module's latest published version, or nil
// when offline or the proxy has nothing to report (a module with no tags or no
// published version answers 404).
func GetLatestPortRelease(ctx context.Context, currentVersion string) (*LatestRelease, error) {
	if os.Getenv("PI_OFFLINE") != "" {
		return nil, nil
	}
	return getLatestPortReleaseFrom(ctx, PortReleaseURL, currentVersion, http.DefaultClient)
}

// getLatestPortReleaseFrom fetches and parses a module proxy @latest document
// from a URL.
func getLatestPortReleaseFrom(ctx context.Context, url, currentVersion string, client *http.Client) (*LatestRelease, error) {
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", PiUserAgent(currentVersion))
	request.Header.Set("accept", "application/json")
	timeoutMS := DefaultVersionCheckTimeoutMS
	response, err := FetchWithRetry(ctx, request, client, FetchRetryOptions{TimeoutMS: &timeoutMS})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Version any `json:"Version"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	version, ok := payload.Version.(string)
	if !ok || strings.TrimSpace(version) == "" {
		return nil, nil
	}
	return &LatestRelease{Version: strings.TrimSpace(version)}, nil
}

// checkForLatestPortReleaseFrom is the check over an explicit endpoint, so a
// test can point it at a local server.
func checkForLatestPortReleaseFrom(ctx context.Context, url, currentVersion string, client *http.Client) *LatestRelease {
	release, err := getLatestPortReleaseFrom(ctx, url, currentVersion, client)
	if err != nil || release == nil {
		return nil
	}
	if IsNewerPackageVersion(release.Version, currentVersion) {
		return release
	}
	return nil
}

// CheckForLatestPortRelease returns a newer release of this module, or nil
// (errors are swallowed like upstream).
func CheckForLatestPortRelease(ctx context.Context, currentVersion string) *LatestRelease {
	if os.Getenv("PI_SKIP_VERSION_CHECK") != "" {
		return nil
	}
	return checkForLatestPortReleaseFrom(ctx, PortReleaseURL, currentVersion, http.DefaultClient)
}
