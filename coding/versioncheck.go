package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Port of utils/version-check.ts, including the semver `valid`/`compare`
// subset the release check needs (the Go port has no semver dependency).

// LatestVersionURL is the release metadata endpoint.
const LatestVersionURL = "https://pi.dev/api/latest-version"

// DefaultVersionCheckTimeoutMS bounds the version request.
const DefaultVersionCheckTimeoutMS int64 = 10_000

// LatestPiRelease is the published release info.
type LatestPiRelease struct {
	Version     string
	PackageName string
	Note        string
}

// FormatVersionCheckError includes the useful errno details hidden behind a
// generic transport error.
func FormatVersionCheckError(err error) string {
	if err == nil {
		return ""
	}
	rootMessage := err.Error()
	codes := []string{}
	var aggregate interface{ Unwrap() []error }
	if errors.As(err, &aggregate) {
		for _, inner := range aggregate.Unwrap() {
			if code := errorCode(inner); code != "" {
				codes = append(codes, code)
			}
		}
	} else if cause := errors.Unwrap(err); cause != nil {
		if code := errorCode(cause); code != "" {
			codes = append(codes, code)
		} else if cause.Error() != "" {
			return fmt.Sprintf("%s (cause: %s)", rootMessage, cause.Error())
		}
	}
	if len(codes) > 0 {
		seen := map[string]bool{}
		var unique []string
		for _, code := range codes {
			if !seen[code] {
				seen[code] = true
				unique = append(unique, code)
			}
		}
		return fmt.Sprintf("%s (%s)", rootMessage, strings.Join(unique, ", "))
	}
	return rootMessage
}

// errorCode extracts an errno-style code from a wrapped error, if any.
func errorCode(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return ""
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

// GetLatestPiRelease fetches the published release, or nil when offline or the
// endpoint reports nothing usable.
func GetLatestPiRelease(ctx context.Context, currentVersion string, options *VersionCheckOptions) (*LatestPiRelease, error) {
	if os.Getenv("PI_OFFLINE") != "" {
		return nil, nil
	}
	return getLatestPiReleaseFrom(ctx, LatestVersionURL, currentVersion, options, http.DefaultClient)
}

// getLatestPiReleaseFrom fetches and parses release metadata from a URL.
func getLatestPiReleaseFrom(ctx context.Context, url, currentVersion string, options *VersionCheckOptions, client *http.Client) (*LatestPiRelease, error) {
	if options == nil {
		options = &VersionCheckOptions{}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", PiUserAgent(currentVersion))
	request.Header.Set("accept", "application/json")
	timeoutMS := DefaultVersionCheckTimeoutMS
	if options.TimeoutMS != nil {
		timeoutMS = *options.TimeoutMS
	}
	retries := 0
	if options.Retry {
		retries = 2
	}
	response, err := FetchWithRetry(ctx, request, http.DefaultClient, FetchRetryOptions{
		MaxRetries: &retries, TimeoutMS: &timeoutMS,
	})
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
		PackageName any `json:"packageName"`
		Version     any `json:"version"`
		Note        any `json:"note"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	version, ok := payload.Version.(string)
	if !ok || strings.TrimSpace(version) == "" {
		return nil, nil
	}
	release := &LatestPiRelease{Version: strings.TrimSpace(version)}
	if name, ok := payload.PackageName.(string); ok && strings.TrimSpace(name) != "" {
		release.PackageName = strings.TrimSpace(name)
	}
	if note, ok := payload.Note.(string); ok && strings.TrimSpace(note) != "" {
		release.Note = strings.TrimSpace(note)
	}
	return release, nil
}

// VersionCheckOptions configure the release check.
type VersionCheckOptions struct {
	TimeoutMS *int64
	Retry     bool
}

// GetLatestPiVersion returns just the published version.
func GetLatestPiVersion(ctx context.Context, currentVersion string, options *VersionCheckOptions) (string, error) {
	release, err := GetLatestPiRelease(ctx, currentVersion, options)
	if err != nil || release == nil {
		return "", err
	}
	return release.Version, nil
}

// CheckForNewPiVersion returns a newer release, or nil (errors are swallowed
// like upstream).
func CheckForNewPiVersion(ctx context.Context, currentVersion string) *LatestPiRelease {
	if os.Getenv("PI_SKIP_VERSION_CHECK") != "" {
		return nil
	}
	release, err := GetLatestPiRelease(ctx, currentVersion, nil)
	if err != nil || release == nil {
		return nil
	}
	if IsNewerPackageVersion(release.Version, currentVersion) {
		return release
	}
	return nil
}
