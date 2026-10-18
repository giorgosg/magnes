package exchange

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var ErrInvalidSourceName = errors.New("invalid source name")

var (
	labelPattern      = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	sourceNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*(:[a-z0-9]+(-[a-z0-9]+)*)*$`)
	nonLabelRun       = regexp.MustCompile(`[^a-z0-9]+`)
)

// ValidOriginLabel reports whether label can name an import's origin: one lowercase
// kebab-case segment, such as alice. An import stores each incoming source as
// <origin>:<source>, so the chain of origins accumulates one segment per hop.
func ValidOriginLabel(label string) bool {
	return labelPattern.MatchString(label)
}

// PrefixSourceName stores an incoming source under an import's origin: dht from alice
// becomes alice:dht, and bob:dht becomes alice:bob:dht.
func PrefixSourceName(origin, source string) string {
	return origin + ":" + source
}

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
