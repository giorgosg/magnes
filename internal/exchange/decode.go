package exchange

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
)

var errNotObject = errors.New("not a JSON object")

// object walks one JSON object, recording which keys a reader knows. Keys are matched
// exactly, unlike encoding/json's case-insensitive struct decoding.
type object struct {
	notes  *notes
	path   string
	fields map[string]json.RawMessage
	known  map[string]struct{}
}

func newObject(n *notes, path string, raw json.RawMessage) (*object, error) {
	var fields map[string]json.RawMessage

	err := errNotObject
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		err = json.Unmarshal(raw, &fields)
	}

	if err != nil {
		if path == "" {
			return nil, err
		}

		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return &object{notes: n, path: path, fields: fields, known: make(map[string]struct{})}, nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}

	return path + "." + key
}

// value returns a known key's raw value. A null is read as absent.
func (o *object) value(key string) (json.RawMessage, bool) {
	o.known[key] = struct{}{}

	raw, ok := o.fields[key]
	if !ok {
		return nil, false
	}

	if isNull(raw) {
		o.notes.add(ReasonNull, joinPath(o.path, key), "")
		return nil, false
	}

	return raw, true
}

// reportUnknownKeys reports every key the reader did not ask for.
func (o *object) reportUnknownKeys() {
	for key := range o.fields {
		if _, ok := o.known[key]; !ok {
			o.notes.add(ReasonUnknownField, joinPath(o.path, key), "")
		}
	}
}

// child opens a known key holding an object. ok is false when the key is absent or null.
func (o *object) child(key string) (*object, bool, error) {
	raw, ok := o.value(key)
	if !ok {
		return nil, false, nil
	}

	child, err := newObject(o.notes, joinPath(o.path, key), raw)

	return child, err == nil, err
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func decodeJSON[T any](raw json.RawMessage) (T, error) {
	var v T

	err := json.Unmarshal(raw, &v)

	return v, err
}

func decodeTime(raw json.RawMessage) (time.Time, error) {
	s, err := decodeJSON[string](raw)
	if err != nil {
		return time.Time{}, err
	}

	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}

	return t.UTC(), nil
}

// optional decodes an optional key. An absent or null key is absent; a value of the
// wrong type is an error.
func optional[T any](o *object, key string, decode func(json.RawMessage) (T, error)) (model.Maybe[T], error) {
	raw, ok := o.value(key)
	if !ok {
		return model.Maybe[T]{}, nil
	}

	v, err := decode(raw)
	if err != nil {
		return model.Maybe[T]{}, fmt.Errorf("%s: %w", joinPath(o.path, key), err)
	}

	return model.MaybeValid(v), nil
}

// required decodes a key that must be present and not null.
func required[T any](o *object, key string, decode func(json.RawMessage) (T, error)) (T, error) {
	v, err := optional(o, key, decode)
	if err != nil {
		return v.Val, err
	}

	if !v.Valid {
		return v.Val, fmt.Errorf("%s: required", joinPath(o.path, key))
	}

	return v.Val, nil
}

// droppable decodes an optional key whose failure drops only that key, for values that
// do not affect how the rest is read. What the failed decode noted is discarded, and the
// key is reported as an invalid entry.
func droppable[T any](o *object, key string, decode func(json.RawMessage) (T, error)) model.Maybe[T] {
	mark := len(o.notes.entries)

	v, err := optional(o, key, decode)
	if err != nil {
		o.notes.entries = o.notes.entries[:mark]
		o.notes.add(ReasonInvalidEntry, joinPath(o.path, key), "")

		return model.Maybe[T]{}
	}

	return v
}

// optionalArray decodes an optional array. A null element is reported and skipped; an
// element decoder returning false drops that element.
func optionalArray[T any](
	o *object,
	key string,
	decode func(n *notes, path string, raw json.RawMessage) (T, bool, error),
) (model.Maybe[[]T], error) {
	raw, ok := o.value(key)
	if !ok {
		return model.Maybe[[]T]{}, nil
	}

	values, err := decodeArray(o.notes, joinPath(o.path, key), raw, decode)
	if err != nil {
		return model.Maybe[[]T]{}, err
	}

	return model.MaybeValid(values), nil
}

// decodeArray decodes the array at field. Its errors already name their path.
func decodeArray[T any](
	n *notes,
	field string,
	raw json.RawMessage,
	decode func(n *notes, path string, raw json.RawMessage) (T, bool, error),
) ([]T, error) {
	path := field + "[]"

	elements, err := decodeJSON[[]json.RawMessage](raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}

	values := make([]T, 0, len(elements))

	for _, element := range elements {
		if isNull(element) {
			n.add(ReasonNull, path, "")
			continue
		}

		v, keep, err := decode(n, path, element)
		if err != nil {
			return nil, err
		}

		if keep {
			values = append(values, v)
		}
	}

	return values, nil
}

func decodeHeader(n *notes, fields *object) (Header, error) {
	var header Header

	format, ok := fields.value("format")
	if !ok {
		return header, ErrUnsupportedFormat
	}

	formatObject, err := newObject(n, "format", format)
	if err != nil {
		return header, fmt.Errorf("%w: %w", ErrUnsupportedFormat, err)
	}

	name, err := required(formatObject, "name", decodeJSON[string])
	if err != nil || name != FormatName {
		return header, fmt.Errorf("%w: %q", ErrUnsupportedFormat, name)
	}

	header.Version, err = required(formatObject, "version", func(raw json.RawMessage) (Version, error) {
		return decodeVersion(n, raw)
	})
	if err != nil {
		return header, fmt.Errorf("%w: %w", ErrInvalidHeader, err)
	}

	// An unknown minor version only adds what this reader can ignore.
	if header.Version.Major != CurrentVersion.Major {
		return header, fmt.Errorf(
			"%w: %d.%d",
			ErrUnsupportedVersion,
			header.Version.Major,
			header.Version.Minor,
		)
	}

	formatObject.reportUnknownKeys()

	// Only format decides how the stream is read, so nothing else in a header is fatal.
	header.Producer = droppable(fields, "producer", func(raw json.RawMessage) (Producer, error) {
		return decodeProducer(n, raw)
	})
	header.ExportedAt = droppable(fields, "exportedAt", decodeTime)
	header.Since = droppable(fields, "since", decodeTime)

	fields.reportUnknownKeys()

	return header, nil
}

func decodeVersion(n *notes, raw json.RawMessage) (Version, error) {
	o, err := newObject(n, "format.version", raw)
	if err != nil {
		return Version{}, err
	}

	var v Version

	if v.Major, err = required(o, "major", decodeJSON[int]); err != nil {
		return v, err
	}

	if v.Minor, err = required(o, "minor", decodeJSON[int]); err != nil {
		return v, err
	}

	o.reportUnknownKeys()

	return v, nil
}

func decodeProducer(n *notes, raw json.RawMessage) (Producer, error) {
	o, err := newObject(n, "producer", raw)
	if err != nil {
		return Producer{}, err
	}

	var p Producer

	if p.Program, err = optional(o, "program", decodeJSON[string]); err != nil {
		return p, err
	}

	if p.Version, err = optional(o, "version", decodeJSON[string]); err != nil {
		return p, err
	}

	o.reportUnknownKeys()

	return p, nil
}

var infoHashPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func decodeInfoHash(raw json.RawMessage) (protocol.ID, error) {
	s, err := decodeJSON[string](raw)
	if err != nil {
		return protocol.ID{}, err
	}

	if !infoHashPattern.MatchString(s) {
		return protocol.ID{}, fmt.Errorf("not 40 lowercase hex digits: %q", s)
	}

	return protocol.ParseID(s)
}

func decodeRecord(fields *object) (Record, error) {
	var (
		rec Record
		err error
	)

	if rec.InfoHash, err = required(fields, "infoHash", decodeInfoHash); err != nil {
		return rec, err
	}

	if rec.Name, err = required(fields, "name", decodeJSON[string]); err != nil {
		return rec, err
	}

	if rec.Size, err = optional(fields, "size", decodeJSON[uint64]); err != nil {
		return rec, err
	}

	if rec.Private, err = optional(fields, "private", decodeJSON[bool]); err != nil {
		return rec, err
	}

	if rec.PublishedAt, err = optional(fields, "publishedAt", decodeTime); err != nil {
		return rec, err
	}

	if rec.Files, err = optionalArray(fields, "files", decodeFile); err != nil {
		return rec, err
	}

	if rec.FilesComplete, err = optional(fields, "filesComplete", decodeJSON[bool]); err != nil {
		return rec, err
	}

	if rec.Sources, err = optionalArray(fields, "sources", decodeSource); err != nil {
		return rec, err
	}

	rec.Magnes = droppable(fields, "magnes", func(raw json.RawMessage) (Magnes, error) {
		return decodeMagnes(fields.notes, raw)
	}).Val

	fields.reportUnknownKeys()

	return rec, nil
}

// A file entry with a field missing is an error, not a dropped entry: dropping it would
// silently turn a complete list into a partial one.
func decodeFile(n *notes, path string, raw json.RawMessage) (File, bool, error) {
	o, err := newObject(n, path, raw)
	if err != nil {
		return File{}, false, err
	}

	var f File

	if f.Index, err = required(o, "index", decodeJSON[uint]); err != nil {
		return f, false, err
	}

	if f.Path, err = required(o, "path", decodeJSON[string]); err != nil {
		return f, false, err
	}

	if f.Size, err = required(o, "size", decodeJSON[uint64]); err != nil {
		return f, false, err
	}

	o.reportUnknownKeys()

	return f, true, nil
}

func decodeSource(n *notes, path string, raw json.RawMessage) (Source, bool, error) {
	o, err := newObject(n, path, raw)
	if err != nil {
		return Source{}, false, err
	}

	var s Source

	name, err := optional(o, "source", decodeJSON[string])
	if err != nil {
		return s, false, err
	}

	s.Name = name.Val

	if s.Seeders, err = optional(o, "seeders", decodeJSON[uint]); err != nil {
		return s, false, err
	}

	if s.Leechers, err = optional(o, "leechers", decodeJSON[uint]); err != nil {
		return s, false, err
	}

	if s.PublishedAt, err = optional(o, "publishedAt", decodeTime); err != nil {
		return s, false, err
	}

	if s.ScrapedAt, err = optional(o, "scrapedAt", decodeTime); err != nil {
		return s, false, err
	}

	o.reportUnknownKeys()

	if !ValidSourceName(s.Name) {
		n.add(ReasonInvalidSourceName, joinPath(path, "source"), s.Name)
		return s, false, nil
	}

	return s, true, nil
}

// Nothing in the vendor section is required to understand the torrent, so a mistyped
// part of it is dropped and reported, and the record is kept.
func decodeMagnes(n *notes, raw json.RawMessage) (Magnes, error) {
	var m Magnes

	o, err := newObject(n, "magnes", raw)
	if err != nil {
		return m, err
	}

	m.Classification = droppable(o, "classification", func(raw json.RawMessage) (Classification, error) {
		return decodeClassification(n, raw)
	})

	m.Tags = droppable(o, "tags", func(raw json.RawMessage) ([]string, error) {
		return decodeArray(n, joinPath(o.path, "tags"), raw, decodeElement[string])
	})

	o.reportUnknownKeys()

	return m, nil
}

func decodeElement[T any](_ *notes, path string, raw json.RawMessage) (T, bool, error) {
	v, err := decodeJSON[T](raw)
	if err != nil {
		return v, false, fmt.Errorf("%s: %w", path, err)
	}

	return v, true, nil
}

// optionalEnum reads a format spelling as its internal value. An unknown spelling is
// read as absent and reported with its value.
func optionalEnum[T ~string](o *object, key string, table enumTable[T]) (model.Maybe[T], error) {
	spelling, err := optional(o, key, decodeJSON[string])
	if err != nil || !spelling.Valid {
		return model.Maybe[T]{}, err
	}

	v, ok := table.internal(spelling.Val)
	if !ok {
		o.notes.add(ReasonUnknownValue, joinPath(o.path, key), spelling.Val)
		return model.Maybe[T]{}, nil
	}

	return model.MaybeValid(v), nil
}

const classificationPath = "magnes.classification"

func decodeClassification(n *notes, raw json.RawMessage) (Classification, error) {
	var c Classification

	o, err := newObject(n, classificationPath, raw)
	if err != nil {
		return c, err
	}

	if c.ContentType, err = optionalEnum(o, "contentType", contentTypes); err != nil {
		return c, err
	}

	if c.Work, err = decodeWork(o); err != nil {
		return c, err
	}

	if c.InferredTitle, err = optional(o, "inferredTitle", decodeJSON[string]); err != nil {
		return c, err
	}

	if c.InferredYear, err = optional(o, "inferredYear", decodeJSON[model.Year]); err != nil {
		return c, err
	}

	if c.Languages, err = optionalArray(o, "languages", decodeLanguage); err != nil {
		return c, err
	}

	if c.Episodes, err = optionalArray(o, "episodes", decodeSeasonEpisodes); err != nil {
		return c, err
	}

	if c.Video, err = decodeVideo(o); err != nil {
		return c, err
	}

	if c.ReleaseGroup, err = optional(o, "releaseGroup", decodeJSON[string]); err != nil {
		return c, err
	}

	o.reportUnknownKeys()

	return c, nil
}

// A work identity is kept only whole: a half identity is dropped and reported.
func decodeWork(classification *object) (model.Maybe[Work], error) {
	o, ok, err := classification.child("work")
	if !ok {
		return model.Maybe[Work]{}, err
	}

	source, err := optional(o, "source", decodeJSON[string])
	if err != nil {
		return model.Maybe[Work]{}, err
	}

	id, err := optional(o, "id", decodeJSON[string])
	if err != nil {
		return model.Maybe[Work]{}, err
	}

	o.reportUnknownKeys()

	if !source.Valid || !id.Valid {
		o.notes.add(ReasonInvalidEntry, o.path, "")
		return model.Maybe[Work]{}, nil
	}

	return model.MaybeValid(Work{Source: source.Val, ID: id.Val}), nil
}

// A language this program does not support is an unknown value.
func decodeLanguage(n *notes, path string, raw json.RawMessage) (model.Language, bool, error) {
	code, err := decodeJSON[string](raw)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", path, err)
	}

	if !supportedLanguage(code) {
		n.add(ReasonUnknownValue, path, code)
		return "", false, nil
	}

	return model.Language(code), true, nil
}

func decodeSeasonEpisodes(n *notes, path string, raw json.RawMessage) (SeasonEpisodes, bool, error) {
	o, err := newObject(n, path, raw)
	if err != nil {
		return SeasonEpisodes{}, false, err
	}

	season, err := optional(o, "season", decodeJSON[int])
	if err != nil {
		return SeasonEpisodes{}, false, err
	}

	episodes, err := optionalArray(o, "episodes", decodeElement[int])
	if err != nil {
		return SeasonEpisodes{}, false, err
	}

	wholeSeason, err := optional(o, "wholeSeason", decodeJSON[bool])
	if err != nil {
		return SeasonEpisodes{}, false, err
	}

	o.reportUnknownKeys()

	entry := SeasonEpisodes{Season: season.Val, Episodes: episodes.Val, WholeSeason: wholeSeason.Val}

	// An empty episodes list is not the episodes form, just as wholeSeason: false is not
	// the whole-season form.
	if len(entry.Episodes) == 0 {
		entry.Episodes = nil
	}

	if !season.Valid || !entry.valid() {
		n.add(ReasonInvalidEntry, path, "")
		return SeasonEpisodes{}, false, nil
	}

	return entry, true, nil
}

func decodeVideo(classification *object) (Video, error) {
	var v Video

	o, ok, err := classification.child("video")
	if !ok {
		return v, err
	}

	if v.Resolution, err = optionalEnum(o, "resolution", videoResolutions); err != nil {
		return v, err
	}

	if v.Source, err = optionalEnum(o, "source", videoSources); err != nil {
		return v, err
	}

	if v.Codec, err = optionalEnum(o, "codec", videoCodecs); err != nil {
		return v, err
	}

	if v.ThreeD, err = optionalEnum(o, "3d", video3Ds); err != nil {
		return v, err
	}

	if v.Modifier, err = optionalEnum(o, "modifier", videoModifiers); err != nil {
		return v, err
	}

	o.reportUnknownKeys()

	return v, nil
}
