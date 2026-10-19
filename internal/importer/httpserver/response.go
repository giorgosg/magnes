package httpserver

import (
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
)

// errorResponse answers a request refused before anything was read.
type errorResponse struct {
	Error string `json:"error"`
}

// reportResponse is importer.ExchangeReport as JSON, in the format's own camelCase. Error
// is set when the import stopped; the counts then cover what was committed before it did.
type reportResponse struct {
	Error     string              `json:"error,omitempty"`
	Headers   []headerResponse    `json:"headers"`
	Records   int                 `json:"records"`
	Malformed []malformedResponse `json:"malformed"`
	Torrents  torrentsResponse    `json:"torrents"`
	Hints     hintsResponse       `json:"hints"`
	TagsAdded int                 `json:"tagsAdded"`
	Dropped   []droppedResponse   `json:"dropped"`
}

type headerResponse struct {
	Line       int               `json:"line"`
	Version    versionResponse   `json:"version"`
	Producer   *producerResponse `json:"producer,omitempty"`
	ExportedAt *time.Time        `json:"exportedAt,omitempty"`
	Since      *time.Time        `json:"since,omitempty"`
}

type versionResponse struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

type producerResponse struct {
	Program string `json:"program,omitempty"`
	Version string `json:"version,omitempty"`
}

type malformedResponse struct {
	Line  int    `json:"line"`
	Error string `json:"error"`
}

type torrentsResponse struct {
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Banned    int `json:"banned"`
	Skipped   int `json:"skipped"`
}

type hintsResponse struct {
	Written int `json:"written"`
	Kept    int `json:"kept"`
}

type droppedResponse struct {
	Scope  exchange.Scope  `json:"scope"`
	Reason exchange.Reason `json:"reason"`
	Path   string          `json:"path"`
	Value  string          `json:"value,omitempty"`
	Count  int             `json:"count"`
}

func newReportResponse(report importer.ExchangeReport) reportResponse {
	response := reportResponse{
		Headers:   make([]headerResponse, 0, len(report.Headers)),
		Records:   report.Records,
		Malformed: make([]malformedResponse, 0, len(report.Malformed)),
		Torrents: torrentsResponse{
			New:       report.New,
			Updated:   report.Updated,
			Unchanged: report.Unchanged,
			Banned:    report.Banned,
			Skipped:   report.Skipped,
		},
		Hints:     hintsResponse{Written: report.HintsWritten, Kept: report.HintsKept},
		TagsAdded: report.TagsAdded,
		Dropped:   make([]droppedResponse, 0, len(report.Dropped)),
	}

	for _, h := range report.Headers {
		header := headerResponse{
			Line:    h.Line,
			Version: versionResponse{Major: h.Header.Version.Major, Minor: h.Header.Version.Minor},
		}

		if h.Header.Producer.Valid {
			header.Producer = &producerResponse{
				Program: h.Header.Producer.Val.Program.Val,
				Version: h.Header.Producer.Val.Version.Val,
			}
		}

		if h.Header.ExportedAt.Valid {
			header.ExportedAt = &h.Header.ExportedAt.Val
		}

		if h.Header.Since.Valid {
			header.Since = &h.Header.Since.Val
		}

		response.Headers = append(response.Headers, header)
	}

	for _, m := range report.Malformed {
		response.Malformed = append(response.Malformed, malformedResponse{Line: m.Line, Error: m.Err.Error()})
	}

	for _, d := range report.Dropped {
		response.Dropped = append(response.Dropped, droppedResponse{
			Scope:  d.Scope,
			Reason: d.Reason,
			Path:   d.Path,
			Value:  d.Value,
			Count:  d.Count,
		})
	}

	return response
}
