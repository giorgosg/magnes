package exchange

import (
	"errors"
	"io"
	"sort"
)

// CheckedHeader is one header and the line it was on.
type CheckedHeader struct {
	Line   int
	Header Header
}

// TagCount counts the records carrying one incoming magnes tag.
type TagCount struct {
	Tag   string
	Count int
}

// CheckResult is what a stream holds and what reading it would drop.
type CheckResult struct {
	Headers   []CheckedHeader
	Records   int
	Malformed []MalformedLineError
	Dropped   []ReportEntry
	// Tags is ordered most frequent first, then by name.
	Tags []TagCount
}

// Check reads a whole stream without importing it. The error is fatal, such as an
// unsupported version; the result still holds everything read before it.
func Check(src io.Reader) (CheckResult, error) {
	var result CheckResult

	reader := NewReader(src)
	tags := make(map[string]int)

	err := func() error {
		for {
			line, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}

			var malformed *MalformedLineError
			if errors.As(err, &malformed) {
				result.Malformed = append(result.Malformed, *malformed)
				continue
			}

			if err != nil {
				return err
			}

			if line.Header != nil {
				result.Headers = append(result.Headers, CheckedHeader{Line: line.Number, Header: *line.Header})
				continue
			}

			result.Records++

			seen := make(map[string]struct{}, len(line.Record.Magnes.Tags.Val))
			for _, tag := range line.Record.Magnes.Tags.Val {
				if _, dup := seen[tag]; !dup {
					seen[tag] = struct{}{}
					tags[tag]++
				}
			}
		}
	}()

	result.Dropped = reader.Report()

	for tag, count := range tags {
		result.Tags = append(result.Tags, TagCount{Tag: tag, Count: count})
	}

	sort.Slice(result.Tags, func(i, j int) bool {
		if result.Tags[i].Count != result.Tags[j].Count {
			return result.Tags[i].Count > result.Tags[j].Count
		}

		return result.Tags[i].Tag < result.Tags[j].Tag
	})

	return result, err
}
