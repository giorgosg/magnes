// Package exchange reads and writes exchange files: versioned NDJSON streams of torrent
// records that other instances and other programs can read. The contract is
// docs/exchange-file.md. The package touches no database; importing and exporting the
// catalogue build on it.
package exchange

import (
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
)

// FormatName is the program-independent name every header carries in format.name.
const FormatName = "torrent-exchange"

// CurrentVersion is the version this package writes. It reads any 1.x.
var CurrentVersion = Version{Major: 1, Minor: 0}

// Version is a format version. A reader refuses an unknown major version and accepts an
// unknown minor one.
type Version struct {
	Major int
	Minor int
}

// Header applies to every record after it, until the next header.
type Header struct {
	// Version is what the header declared. Writing always declares CurrentVersion.
	Version    Version
	Producer   model.Maybe[Producer]
	ExportedAt model.Maybe[time.Time]
	// Since is present only on an incremental export.
	Since model.Maybe[time.Time]
}

// Producer identifies the writing program. It does not affect how a file is read.
type Producer struct {
	Program model.Maybe[string]
	Version model.Maybe[string]
}

// Record is one torrent. Every model.Maybe field distinguishes absent, which means
// unknown, from a zero, false or empty value.
type Record struct {
	InfoHash    protocol.ID
	Name        string
	Size        model.Maybe[uint64]
	Private     model.Maybe[bool]
	PublishedAt model.Maybe[time.Time]
	// Files absent means the file list is unknown.
	Files model.Maybe[[]File]
	// FilesComplete false means Files is partial. Absent means complete when Files is
	// present.
	FilesComplete model.Maybe[bool]
	Sources       model.Maybe[[]Source]
	Magnes        Magnes
}

// File is one entry of a torrent's file list. All three fields are required.
type File struct {
	Index uint
	Path  string
	Size  uint64
}

// Source is one source that observed the torrent. Name must be a valid source name.
type Source struct {
	Name        string
	Seeders     model.Maybe[uint]
	Leechers    model.Maybe[uint]
	PublishedAt model.Maybe[time.Time]
	ScrapedAt   model.Maybe[time.Time]
}

// Magnes is the vendor section. It is omitted from the output when it holds nothing.
type Magnes struct {
	Classification model.Maybe[Classification]
	Tags           model.Maybe[[]string]
}

// Classification is the producer's current classification, carried as internal values.
// Enum fields are translated to and from the format's own spellings.
type Classification struct {
	ContentType   model.Maybe[model.ContentType]
	Work          model.Maybe[Work]
	InferredTitle model.Maybe[string]
	InferredYear  model.Maybe[model.Year]
	Languages     model.Maybe[[]model.Language]
	Episodes      model.Maybe[[]SeasonEpisodes]
	Video         Video
	ReleaseGroup  model.Maybe[string]
}

// Work is a work identity, always backed by a catalogue provider's record.
type Work struct {
	Source string
	ID     string
}

// Video groups the video release attributes. It is omitted when it holds nothing.
type Video struct {
	Resolution model.Maybe[model.VideoResolution]
	Source     model.Maybe[model.VideoSource]
	Codec      model.Maybe[model.VideoCodec]
	ThreeD     model.Maybe[model.Video3D]
	Modifier   model.Maybe[model.VideoModifier]
}

// SeasonEpisodes is either some episodes of a season or the whole season, never both.
// Season 0 holds specials.
type SeasonEpisodes struct {
	Season      int
	Episodes    []int
	WholeSeason bool
}
