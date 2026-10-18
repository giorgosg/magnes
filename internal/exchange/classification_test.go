package exchange_test

import (
	"testing"

	"github.com/bitmagnet-io/bitmagnet/internal/exchange"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readClassification(t *testing.T, classification string) (exchange.Record, []exchange.ReportEntry) {
	t.Helper()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"one","magnes":{"classification":` + classification + `}}` + "\n"

	lines, malformed, err, reader := readAll(t, input)
	require.NoError(t, err)
	require.Empty(t, malformed)
	require.Len(t, lines, 2)

	return *lines[1].Record, reader.Report()
}

// The spellings are the spec's table in docs/exchange-file.md, written out by hand so the
// test cannot agree with the code by construction.
func TestEnumSpellingsReadAsInternalValues(t *testing.T) {
	t.Parallel()

	type spelling struct {
		classification string
		check          func(exchange.Classification) any
		want           any
	}

	contentType := func(c exchange.Classification) any { return c.ContentType.Val }
	resolution := func(c exchange.Classification) any { return c.Video.Resolution.Val }
	source := func(c exchange.Classification) any { return c.Video.Source.Val }
	codec := func(c exchange.Classification) any { return c.Video.Codec.Val }
	threeD := func(c exchange.Classification) any { return c.Video.ThreeD.Val }
	modifier := func(c exchange.Classification) any { return c.Video.Modifier.Val }

	cases := []spelling{}
	for format, internal := range map[string]model.ContentType{
		"movie": model.ContentTypeMovie, "tv-show": model.ContentTypeTvShow, "music": model.ContentTypeMusic,
		"ebook": model.ContentTypeEbook, "comic": model.ContentTypeComic, "audiobook": model.ContentTypeAudiobook,
		"game": model.ContentTypeGame, "software": model.ContentTypeSoftware, "xxx": model.ContentTypeXxx,
	} {
		cases = append(cases, spelling{`{"contentType":"` + format + `"}`, contentType, internal})
	}

	for format, internal := range map[string]model.VideoResolution{
		"360p": model.VideoResolutionV360p, "480p": model.VideoResolutionV480p,
		"540p": model.VideoResolutionV540p, "576p": model.VideoResolutionV576p,
		"720p": model.VideoResolutionV720p, "1080p": model.VideoResolutionV1080p,
		"1440p": model.VideoResolutionV1440p, "2160p": model.VideoResolutionV2160p,
		"4320p": model.VideoResolutionV4320p,
	} {
		cases = append(cases, spelling{`{"video":{"resolution":"` + format + `"}}`, resolution, internal})
	}

	for format, internal := range map[string]model.VideoSource{
		"cam": model.VideoSourceCAM, "telesync": model.VideoSourceTELESYNC, "telecine": model.VideoSourceTELECINE,
		"workprint": model.VideoSourceWORKPRINT, "dvd": model.VideoSourceDVD, "tv": model.VideoSourceTV,
		"web-dl": model.VideoSourceWEBDL, "webrip": model.VideoSourceWEBRip, "bluray": model.VideoSourceBluRay,
	} {
		cases = append(cases, spelling{`{"video":{"source":"` + format + `"}}`, source, internal})
	}

	for format, internal := range map[string]model.VideoCodec{
		"av1": model.VideoCodecAV1, "h264": model.VideoCodecH264, "x264": model.VideoCodecX264,
		"x265": model.VideoCodecX265, "xvid": model.VideoCodecXviD, "divx": model.VideoCodecDivX,
		"mpeg2": model.VideoCodecMPEG2, "mpeg4": model.VideoCodecMPEG4,
	} {
		cases = append(cases, spelling{`{"video":{"codec":"` + format + `"}}`, codec, internal})
	}

	for format, internal := range map[string]model.Video3D{
		"3d": model.Video3DV3D, "3d-sbs": model.Video3DV3DSBS, "3d-ou": model.Video3DV3DOU,
	} {
		cases = append(cases, spelling{`{"video":{"3d":"` + format + `"}}`, threeD, internal})
	}

	for format, internal := range map[string]model.VideoModifier{
		"regional": model.VideoModifierREGIONAL, "screener": model.VideoModifierSCREENER,
		"rawhd": model.VideoModifierRAWHD, "brdisk": model.VideoModifierBRDISK, "remux": model.VideoModifierREMUX,
	} {
		cases = append(cases, spelling{`{"video":{"modifier":"` + format + `"}}`, modifier, internal})
	}

	for _, tc := range cases {
		rec, report := readClassification(t, tc.classification)
		require.True(t, rec.Magnes.Classification.Valid, tc.classification)
		assert.Equal(t, tc.want, tc.check(rec.Magnes.Classification.Val), tc.classification)
		assert.Empty(t, report, tc.classification)
	}
}

func TestUnknownEnumValueIsDroppedAndReported(t *testing.T) {
	t.Parallel()

	rec, report := readClassification(t,
		`{"contentType":"podcast","video":{"resolution":"1080p","codec":"hevc","source":"WEBDL"},"releaseGroup":"GRP"}`)

	c := rec.Magnes.Classification.Val
	assert.False(t, c.ContentType.Valid)
	assert.False(t, c.Video.Codec.Valid)
	assert.False(t, c.Video.Source.Valid, "the internal spelling is not the format's")
	assert.Equal(t, model.MaybeValid(model.VideoResolutionV1080p), c.Video.Resolution, "the rest is kept")
	assert.Equal(t, model.MaybeValid("GRP"), c.ReleaseGroup)

	assert.Equal(t, []exchange.ReportEntry{
		{
			Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownValue,
			Path: "magnes.classification.contentType", Value: "podcast", Count: 1,
		},
		{
			Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownValue,
			Path: "magnes.classification.video.codec", Value: "hevc", Count: 1,
		},
		{
			Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownValue,
			Path: "magnes.classification.video.source", Value: "WEBDL", Count: 1,
		},
	}, report)
}

func TestLanguagesOutsideISO6391AreDroppedAndReported(t *testing.T) {
	t.Parallel()

	rec, report := readClassification(t, `{"languages":["en","xx","EN","eng","fr"]}`)

	assert.Equal(t,
		model.MaybeValid([]model.Language{"en", "fr"}),
		rec.Magnes.Classification.Val.Languages,
	)

	path := "magnes.classification.languages[]"
	assert.Equal(t, []exchange.ReportEntry{
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownValue, Path: path, Value: "EN", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownValue, Path: path, Value: "eng", Count: 1},
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownValue, Path: path, Value: "xx", Count: 1},
	}, report)
}

func TestEpisodesEntriesAreEpisodesOrWholeSeasonExactlyOne(t *testing.T) {
	t.Parallel()

	rec, report := readClassification(t, `{"episodes":[`+
		`{"season":0,"episodes":[1,2]},`+
		`{"season":2,"wholeSeason":true},`+
		`{"season":3,"episodes":[1],"wholeSeason":true},`+
		`{"season":4},`+
		`{"season":5,"wholeSeason":false},`+
		`{"season":6,"episodes":[]},`+
		`{"episodes":[1]},`+
		`{"season":-1,"wholeSeason":true},`+
		`{"season":7,"episodes":[-2]}`+
		`]}`)

	assert.Equal(t, model.MaybeValid([]exchange.SeasonEpisodes{
		{Season: 0, Episodes: []int{1, 2}},
		{Season: 2, WholeSeason: true},
	}), rec.Magnes.Classification.Val.Episodes)

	assert.Equal(t, []exchange.ReportEntry{
		{
			Scope: exchange.ScopeRecord, Reason: exchange.ReasonInvalidEntry,
			Path: "magnes.classification.episodes[]", Count: 7,
		},
	}, report)
}

func TestWorkIdentityNeedsBothSourceAndID(t *testing.T) {
	t.Parallel()

	rec, report := readClassification(t, `{"work":{"source":"tmdb","id":"123"},"inferredTitle":"X","inferredYear":2021}`)
	c := rec.Magnes.Classification.Val
	assert.Equal(t, model.MaybeValid(exchange.Work{Source: "tmdb", ID: "123"}), c.Work)
	assert.Equal(t, model.MaybeValid("X"), c.InferredTitle)
	assert.Equal(t, model.MaybeValid(model.Year(2021)), c.InferredYear)
	assert.Empty(t, report)

	rec, report = readClassification(t, `{"work":{"source":"tmdb"},"inferredYear":2021}`)
	c = rec.Magnes.Classification.Val
	assert.False(t, c.Work.Valid, "a half identity is not kept")
	assert.True(t, c.InferredYear.Valid, "the rest of the classification is kept")
	assert.Equal(t, []exchange.ReportEntry{
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonInvalidEntry, Path: "magnes.classification.work", Count: 1},
	}, report)
}

func TestMagnesTagsAreRead(t *testing.T) {
	t.Parallel()

	input := header10 + "\n" +
		`{"infoHash":"` + hashA + `","name":"one","magnes":{"tags":["4k-candidate","keep"],"other":1}}` + "\n"

	lines, _, err, reader := readAll(t, input)
	require.NoError(t, err)
	require.Len(t, lines, 2)

	magnes := lines[1].Record.Magnes
	assert.Equal(t, model.MaybeValid([]string{"4k-candidate", "keep"}), magnes.Tags)
	assert.False(t, magnes.Classification.Valid)
	assert.Equal(t, []exchange.ReportEntry{
		{Scope: exchange.ScopeRecord, Reason: exchange.ReasonUnknownField, Path: "magnes.other", Count: 1},
	}, reader.Report())
}
