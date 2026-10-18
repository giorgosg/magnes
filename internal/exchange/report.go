package exchange

import "sort"

// Scope says whether a report entry came from a header or a record.
type Scope string

const (
	ScopeHeader Scope = "header"
	ScopeRecord Scope = "record"
)

// Reason says why something was dropped.
type Reason string

const (
	// ReasonUnknownField is a field this reader does not know. Its value is ignored.
	ReasonUnknownField Reason = "unknown-field"
	// ReasonNull is a null, read as absent.
	ReasonNull Reason = "null"
	// ReasonUnknownValue is an enum value or language code this reader does not know.
	// The field is read as absent and the rest of the record is kept.
	ReasonUnknownValue Reason = "unknown-value"
	// ReasonInvalidSourceName is a source entry whose name breaks the label rule. The
	// entry is dropped.
	ReasonInvalidSourceName Reason = "invalid-source-name"
	// ReasonInvalidEntry is something dropped because it breaks its shape rule or holds a
	// wrong JSON type where that does not affect how the rest is read: an episodes entry
	// with both or neither form, a work missing source or id, a mistyped header producer,
	// exportedAt or since, or a mistyped magnes section, classification or tags.
	ReasonInvalidEntry Reason = "invalid-entry"
)

// ReportEntry counts one kind of drop at one array-index-free JSON path, such as
// sources[].completed. Value is set for unknown values and invalid names.
type ReportEntry struct {
	Scope  Scope
	Reason Reason
	Path   string
	Value  string
	Count  int
}

type reportKey struct {
	scope  Scope
	reason Reason
	path   string
	value  string
}

// notes collects one line's drops. They join the stream report only when the line
// decodes, so a malformed line contributes nothing but its error.
type notes struct {
	entries []reportKey
	scope   Scope
}

func (n *notes) add(reason Reason, path, value string) {
	n.entries = append(n.entries, reportKey{scope: n.scope, reason: reason, path: path, value: value})
}

type report map[reportKey]int

func (r report) merge(n *notes) {
	for _, key := range n.entries {
		r[key]++
	}
}

func (r report) entries() []ReportEntry {
	entries := make([]ReportEntry, 0, len(r))
	for key, count := range r {
		entries = append(entries, ReportEntry{
			Scope:  key.scope,
			Reason: key.reason,
			Path:   key.path,
			Value:  key.value,
			Count:  count,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}

		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}

		if a.Path != b.Path {
			return a.Path < b.Path
		}

		return a.Value < b.Value
	})

	return entries
}

// Report returns what the stream dropped so far, in a stable order.
func (r *Reader) Report() []ReportEntry {
	return r.report.entries()
}
