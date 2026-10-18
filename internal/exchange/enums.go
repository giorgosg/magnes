package exchange

import (
	"errors"
	"fmt"

	"github.com/bitmagnet-io/bitmagnet/internal/model"
)

// ErrUnmappedValue refuses to write an internal enum value the format has no spelling
// for, rather than leak an internal spelling into the file.
var ErrUnmappedValue = errors.New("internal value has no exchange-format spelling")

// enumTable translates between the format's spelling and an internal value. The
// format's vocabulary is its own, lowercase kebab-case; adding a value is a minor
// version bump, and removing or renaming one is a major bump.
type enumTable[T ~string] struct {
	field      string
	toInternal map[string]T
	toFormat   map[T]string
}

func newEnumTable[T ~string](field string, spellings map[string]T) enumTable[T] {
	table := enumTable[T]{field: field, toInternal: spellings, toFormat: make(map[T]string, len(spellings))}
	for spelling, internal := range spellings {
		table.toFormat[internal] = spelling
	}

	return table
}

func (t enumTable[T]) format(v T) (string, error) {
	spelling, ok := t.toFormat[v]
	if !ok {
		return "", fmt.Errorf("%w: %s %q", ErrUnmappedValue, t.field, string(v))
	}

	return spelling, nil
}

func (t enumTable[T]) internal(spelling string) (T, bool) {
	v, ok := t.toInternal[spelling]
	return v, ok
}

// The internal spellings stay out of the published contract; this table is where they
// meet. The video resolution and 3D internals carry a leading V, and video sources and
// codecs use mixed case.
var (
	contentTypes = newEnumTable("contentType", map[string]model.ContentType{
		"movie":     model.ContentTypeMovie,
		"tv-show":   model.ContentTypeTvShow,
		"music":     model.ContentTypeMusic,
		"ebook":     model.ContentTypeEbook,
		"comic":     model.ContentTypeComic,
		"audiobook": model.ContentTypeAudiobook,
		"game":      model.ContentTypeGame,
		"software":  model.ContentTypeSoftware,
		"xxx":       model.ContentTypeXxx,
	})
	videoResolutions = newEnumTable("video.resolution", map[string]model.VideoResolution{
		"360p":  model.VideoResolutionV360p,
		"480p":  model.VideoResolutionV480p,
		"540p":  model.VideoResolutionV540p,
		"576p":  model.VideoResolutionV576p,
		"720p":  model.VideoResolutionV720p,
		"1080p": model.VideoResolutionV1080p,
		"1440p": model.VideoResolutionV1440p,
		"2160p": model.VideoResolutionV2160p,
		"4320p": model.VideoResolutionV4320p,
	})
	videoSources = newEnumTable("video.source", map[string]model.VideoSource{
		"cam":       model.VideoSourceCAM,
		"telesync":  model.VideoSourceTELESYNC,
		"telecine":  model.VideoSourceTELECINE,
		"workprint": model.VideoSourceWORKPRINT,
		"dvd":       model.VideoSourceDVD,
		"tv":        model.VideoSourceTV,
		"web-dl":    model.VideoSourceWEBDL,
		"webrip":    model.VideoSourceWEBRip,
		"bluray":    model.VideoSourceBluRay,
	})
	videoCodecs = newEnumTable("video.codec", map[string]model.VideoCodec{
		"av1":   model.VideoCodecAV1,
		"h264":  model.VideoCodecH264,
		"x264":  model.VideoCodecX264,
		"x265":  model.VideoCodecX265,
		"xvid":  model.VideoCodecXviD,
		"divx":  model.VideoCodecDivX,
		"mpeg2": model.VideoCodecMPEG2,
		"mpeg4": model.VideoCodecMPEG4,
	})
	video3Ds = newEnumTable("video.3d", map[string]model.Video3D{
		"3d":     model.Video3DV3D,
		"3d-sbs": model.Video3DV3DSBS,
		"3d-ou":  model.Video3DV3DOU,
	})
	videoModifiers = newEnumTable("video.modifier", map[string]model.VideoModifier{
		"regional": model.VideoModifierREGIONAL,
		"screener": model.VideoModifierSCREENER,
		"rawhd":    model.VideoModifierRAWHD,
		"brdisk":   model.VideoModifierBRDISK,
		"remux":    model.VideoModifierREMUX,
	})
)
