package exchange

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
)

// ErrInvalidEpisodes refuses an episodes entry that is not exactly one of some episodes
// or the whole season.
var ErrInvalidEpisodes = errors.New("invalid episodes entry")

// Writer encodes an exchange stream, one header or record per line. It refuses to write
// anything a reader would drop or report, so its output always reads back unchanged.
type Writer struct {
	dst        io.Writer
	wroteFirst bool
}

func NewWriter(dst io.Writer) *Writer {
	return &Writer{dst: dst}
}

// WriteHeader writes a header declaring CurrentVersion. Header.Version is ignored.
func (w *Writer) WriteHeader(h Header) error {
	wire := wireHeader{
		Format: wireFormat{
			Name:    FormatName,
			Version: wireVersion(CurrentVersion),
		},
		ExportedAt: timePtr(h.ExportedAt),
		Since:      timePtr(h.Since),
	}

	if h.Producer.Valid {
		wire.Producer = &wireProducer{
			Program: h.Producer.Val.Program.Addr(),
			Version: h.Producer.Val.Version.Addr(),
		}
	}

	if err := w.writeLine(wire); err != nil {
		return err
	}

	w.wroteFirst = true

	return nil
}

// WriteRecord writes one record under the last header written. Nothing is written when
// it returns an error.
func (w *Writer) WriteRecord(r Record) error {
	if !w.wroteFirst {
		return ErrRecordBeforeHeader
	}

	wire, err := toWireRecord(r)
	if err != nil {
		return fmt.Errorf("%s: %w", r.InfoHash, err)
	}

	return w.writeLine(wire)
}

func (w *Writer) writeLine(v any) error {
	var buf bytes.Buffer

	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(v); err != nil {
		return err
	}

	_, err := w.dst.Write(buf.Bytes())

	return err
}

// The wire types fix field order and spelling. A nil pointer is an absent field; a
// pointer to a zero value is a known zero.
type (
	wireHeader struct {
		Format     wireFormat    `json:"format"`
		Producer   *wireProducer `json:"producer,omitempty"`
		ExportedAt *string       `json:"exportedAt,omitempty"`
		Since      *string       `json:"since,omitempty"`
	}
	wireFormat struct {
		Name    string      `json:"name"`
		Version wireVersion `json:"version"`
	}
	wireVersion struct {
		Major int `json:"major"`
		Minor int `json:"minor"`
	}
	wireProducer struct {
		Program *string `json:"program,omitempty"`
		Version *string `json:"version,omitempty"`
	}
	wireRecord struct {
		InfoHash      string        `json:"infoHash"`
		Name          string        `json:"name"`
		Size          *uint64       `json:"size,omitempty"`
		Private       *bool         `json:"private,omitempty"`
		PublishedAt   *string       `json:"publishedAt,omitempty"`
		Files         *[]wireFile   `json:"files,omitempty"`
		FilesComplete *bool         `json:"filesComplete,omitempty"`
		Sources       *[]wireSource `json:"sources,omitempty"`
		Magnes        *wireMagnes   `json:"magnes,omitempty"`
	}
	wireFile struct {
		Index uint   `json:"index"`
		Path  string `json:"path"`
		Size  uint64 `json:"size"`
	}
	wireSource struct {
		Source      string  `json:"source"`
		Seeders     *uint   `json:"seeders,omitempty"`
		Leechers    *uint   `json:"leechers,omitempty"`
		PublishedAt *string `json:"publishedAt,omitempty"`
		ScrapedAt   *string `json:"scrapedAt,omitempty"`
	}
	wireMagnes struct {
		Classification *wireClassification `json:"classification,omitempty"`
		Tags           *[]string           `json:"tags,omitempty"`
	}
	wireClassification struct {
		ContentType   *string       `json:"contentType,omitempty"`
		Work          *wireWork     `json:"work,omitempty"`
		InferredTitle *string       `json:"inferredTitle,omitempty"`
		InferredYear  *model.Year   `json:"inferredYear,omitempty"`
		Languages     *[]string     `json:"languages,omitempty"`
		Episodes      *[]wireSeason `json:"episodes,omitempty"`
		Video         *wireVideo    `json:"video,omitempty"`
		ReleaseGroup  *string       `json:"releaseGroup,omitempty"`
	}
	wireWork struct {
		Source string `json:"source"`
		ID     string `json:"id"`
	}
	wireSeason struct {
		Season      int    `json:"season"`
		Episodes    *[]int `json:"episodes,omitempty"`
		WholeSeason *bool  `json:"wholeSeason,omitempty"`
	}
	wireVideo struct {
		Resolution *string `json:"resolution,omitempty"`
		Source     *string `json:"source,omitempty"`
		Codec      *string `json:"codec,omitempty"`
		ThreeD     *string `json:"3d,omitempty"`
		Modifier   *string `json:"modifier,omitempty"`
	}
)

func timePtr(t model.Maybe[time.Time]) *string {
	if !t.Valid {
		return nil
	}

	s := t.Val.UTC().Format(time.RFC3339Nano)

	return &s
}

func mapSlice[T, W any](in model.Maybe[[]T], convert func(T) (W, error)) (*[]W, error) {
	if !in.Valid {
		return nil, nil //nolint:nilnil // nil is the absent field
	}

	out := make([]W, 0, len(in.Val))

	for _, v := range in.Val {
		w, err := convert(v)
		if err != nil {
			return nil, err
		}

		out = append(out, w)
	}

	return &out, nil
}

func toWireRecord(r Record) (wireRecord, error) {
	wire := wireRecord{
		InfoHash:      r.InfoHash.String(),
		Name:          r.Name,
		Size:          r.Size.Addr(),
		Private:       r.Private.Addr(),
		PublishedAt:   timePtr(r.PublishedAt),
		FilesComplete: r.FilesComplete.Addr(),
	}

	wire.Files, _ = mapSlice(r.Files, func(f File) (wireFile, error) {
		return wireFile(f), nil
	})

	var err error

	if wire.Sources, err = mapSlice(r.Sources, toWireSource); err != nil {
		return wire, err
	}

	if wire.Magnes, err = toWireMagnes(r.Magnes); err != nil {
		return wire, err
	}

	return wire, nil
}

func toWireSource(s Source) (wireSource, error) {
	if !ValidSourceName(s.Name) {
		return wireSource{}, fmt.Errorf("%w: %q", ErrInvalidSourceName, s.Name)
	}

	return wireSource{
		Source:      s.Name,
		Seeders:     s.Seeders.Addr(),
		Leechers:    s.Leechers.Addr(),
		PublishedAt: timePtr(s.PublishedAt),
		ScrapedAt:   timePtr(s.ScrapedAt),
	}, nil
}

func toWireMagnes(m Magnes) (*wireMagnes, error) {
	if !m.Classification.Valid && !m.Tags.Valid {
		return nil, nil //nolint:nilnil // nil is the absent section
	}

	wire := &wireMagnes{Tags: m.Tags.Addr()}

	if m.Classification.Valid {
		classification, err := toWireClassification(m.Classification.Val)
		if err != nil {
			return nil, err
		}

		wire.Classification = &classification
	}

	return wire, nil
}

func enumPtr[T ~string](v model.Maybe[T], table enumTable[T]) (*string, error) {
	if !v.Valid {
		return nil, nil //nolint:nilnil // nil is the absent field
	}

	spelling, err := table.format(v.Val)
	if err != nil {
		return nil, err
	}

	return &spelling, nil
}

func toWireClassification(c Classification) (wireClassification, error) {
	wire := wireClassification{
		InferredTitle: c.InferredTitle.Addr(),
		InferredYear:  c.InferredYear.Addr(),
		ReleaseGroup:  c.ReleaseGroup.Addr(),
	}

	if c.Work.Valid {
		wire.Work = &wireWork{Source: c.Work.Val.Source, ID: c.Work.Val.ID}
	}

	var err error

	if wire.ContentType, err = enumPtr(c.ContentType, contentTypes); err != nil {
		return wire, err
	}

	if wire.Languages, err = mapSlice(c.Languages, func(lang model.Language) (string, error) {
		if !lang.IsValid() {
			return "", fmt.Errorf("%w: language %q", ErrUnmappedValue, string(lang))
		}

		return lang.Alpha2(), nil
	}); err != nil {
		return wire, err
	}

	if wire.Episodes, err = mapSlice(c.Episodes, toWireSeason); err != nil {
		return wire, err
	}

	if wire.Video, err = toWireVideo(c.Video); err != nil {
		return wire, err
	}

	return wire, nil
}

func toWireSeason(s SeasonEpisodes) (wireSeason, error) {
	valid := s.Season >= 0 && (len(s.Episodes) > 0) != s.WholeSeason
	for _, episode := range s.Episodes {
		valid = valid && episode >= 0
	}

	if !valid {
		return wireSeason{}, fmt.Errorf("%w: season %d", ErrInvalidEpisodes, s.Season)
	}

	wire := wireSeason{Season: s.Season}

	if s.WholeSeason {
		wholeSeason := true
		wire.WholeSeason = &wholeSeason
	} else {
		episodes := s.Episodes
		wire.Episodes = &episodes
	}

	return wire, nil
}

func toWireVideo(v Video) (*wireVideo, error) {
	var (
		wire wireVideo
		err  error
	)

	if wire.Resolution, err = enumPtr(v.Resolution, videoResolutions); err != nil {
		return nil, err
	}

	if wire.Source, err = enumPtr(v.Source, videoSources); err != nil {
		return nil, err
	}

	if wire.Codec, err = enumPtr(v.Codec, videoCodecs); err != nil {
		return nil, err
	}

	if wire.ThreeD, err = enumPtr(v.ThreeD, video3Ds); err != nil {
		return nil, err
	}

	if wire.Modifier, err = enumPtr(v.Modifier, videoModifiers); err != nil {
		return nil, err
	}

	if wire == (wireVideo{}) {
		return nil, nil //nolint:nilnil // nil is the absent group
	}

	return &wire, nil
}
