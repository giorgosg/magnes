package exchange

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrInvalidSourceName = errors.New("invalid source name")

var (
	sourceNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*(:[a-z0-9]+(-[a-z0-9]+)*)*$`)
	nonLabelRun       = regexp.MustCompile(`[^a-z0-9]+`)
)

// ValidSourceName reports whether name follows the label rule: lowercase kebab-case
// segments joined by ':', such as dht or alice:bob:torrents-csv.
func ValidSourceName(name string) bool {
	return sourceNamePattern.MatchString(name)
}

// NormalizeSourceName rewrites a stored source name to follow the label rule, for the
// exporter: Torrents CSV becomes torrents-csv. Within each ':'-separated segment, every
// run of characters other than a-z and 0-9 becomes one hyphen, after lowercasing. It
// fails when a segment has nothing left.
func NormalizeSourceName(name string) (string, error) {
	segments := strings.Split(name, ":")
	for i, segment := range segments {
		segment = nonLabelRun.ReplaceAllString(strings.ToLower(segment), "-")
		segment = strings.Trim(segment, "-")

		if segment == "" {
			return "", fmt.Errorf("%w: %q", ErrInvalidSourceName, name)
		}

		segments[i] = segment
	}

	return strings.Join(segments, ":"), nil
}
