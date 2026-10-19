package importer_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/classifier"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dbtest"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These cover the moderation cases the legacy flat-format importer was tested for, against
// the exchange import that replaced it.

func TestExchangeImportPendingRetainsEvidenceAndLocalAllowIngests(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ledger := moderation.NewService(db.Gorm)
	pending, allowed := protocol.ID{85}, protocol.ID{86}
	require.NoError(t,
		ledger.Flag(t.Context(), moderation.IdentifierForInfoHash(pending), moderation.Evidence{}, "synthetic"))
	require.NoError(t, ledger.Allow(t.Context(), moderation.IdentifierForInfoHash(allowed), "synthetic"))
	require.NoError(t, ledger.ApplyImport(
		t.Context(), moderation.IdentifierForInfoHash(allowed), moderation.Moderator, "synthetic"))

	importExchange(t, db, alice(),
		`{"infoHash":"`+hashHex(85)+`","name":"Pending handbook"}`,
		`{"infoHash":"`+hashHex(86)+`","name":"Allowed handbook"}`,
	)

	record, err := ledger.Get(t.Context(), moderation.IdentifierForInfoHash(pending))
	require.NoError(t, err)
	require.NotNil(t, record.Evidence)
	require.Equal(t, "Pending handbook", record.Evidence.Name)

	exec(t, db, `DELETE FROM torrents WHERE info_hash = $1`, pending[:])

	record, err = ledger.Get(t.Context(), moderation.IdentifierForInfoHash(pending))
	require.NoError(t, err)
	require.Equal(t, "Pending handbook", record.Evidence.Name)
	require.Equal(t, 1, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`, allowed[:]))
}

func TestExchangeImportWaitsForARacingBan(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	ledger := moderation.NewService(db.Gorm)
	hash := protocol.ID{87}
	decisionReady, release := make(chan struct{}), make(chan struct{})

	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	require.NoError(t, db.Gorm.Callback().Create().Before("gorm:create").Register(
		"pause_synthetic_ban", func(tx *gorm.DB) {
			if h, ok := tx.Statement.Dest.(*model.ModerationHistory); ok && h.Action == "ban" {
				close(decisionReady)
				<-release
			}
		}))

	banned := make(chan error, 1)
	go func() { banned <- ledger.Ban(t.Context(), moderation.IdentifierForInfoHash(hash), "synthetic") }()

	select {
	case <-decisionReady:
	case <-time.After(2 * time.Second):
		t.Fatal("ban did not begin")
	}

	imported := make(chan error, 1)

	go func() {
		_, err := newExchangeImporter(db).Import(t.Context(),
			exchangeFile(`{"infoHash":"`+hashHex(87)+`","name":"Racing handbook"}`), alice())
		imported <- err
	}()

	select {
	case <-imported:
		t.Error("ingestion completed while the ban was uncommitted")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	require.NoError(t, <-banned)

	select {
	case err := <-imported:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("import did not finish")
	}

	require.Zero(t, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`, hash[:]),
		"a racing ban must prevent catalogue resurrection")
}

func TestExchangeImportOfABannedTorrentPreservesExistingCataloguePaths(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{97}
	seedTorrent(t, db, hash, "Existing handbook", 1024, false, "multi")
	exec(t, db, `UPDATE torrents SET files_count = 1 WHERE info_hash = $1`, hash[:])
	exec(t, db, `INSERT INTO torrent_files (info_hash,index,path,size,created_at,updated_at)
VALUES ($1,0,'full/private/handbook.pdf',1024,now(),now())`, hash[:])

	ledger := moderation.NewService(db.Gorm)
	id := moderation.IdentifierForInfoHash(hash)
	require.NoError(t, ledger.ApplyImport(t.Context(), id, moderation.Moderator, "synthetic"))

	observed := `{"infoHash":"` + hashHex(97) + `","name":"Observed handbook"}`
	require.Equal(t, 1, importExchange(t, db, alice(), observed).Banned)

	record, err := ledger.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, []string{"full/private/handbook.pdf"}, record.Evidence.Files)

	require.NoError(t, ledger.RetainEvidence(t.Context(), id, moderation.Evidence{
		Files: []string{"full/private/handbook.pdf", "uncapped/second.pdf"},
	}))
	importExchange(t, db, alice(), observed)
	exec(t, db, `DELETE FROM torrents WHERE info_hash = $1`, hash[:])

	record, err = ledger.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "Observed handbook", record.Evidence.Name)
	require.Equal(t, []string{"full/private/handbook.pdf", "uncapped/second.pdf"}, record.Evidence.Files)
}

func TestExchangeImportCannotResurrectADigestOnlyBan(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{81}
	ledger := moderation.NewService(db.Gorm)
	data, err := json.Marshal(moderation.BanFile{
		Version:          1,
		IdentifierFormat: moderation.IdentifierFormatV1,
		Entries: []moderation.BanEntry{
			{Digest: moderation.IdentifierForInfoHash(hash).String(), Origin: moderation.Moderator},
		},
	})
	require.NoError(t, err)
	_, err = ledger.Import(t.Context(), bytes.NewReader(data))
	require.NoError(t, err)

	importExchange(t, db, alice(), `{"infoHash":"`+hashHex(81)+`","name":"Synthetic handbook"}`)

	require.Zero(t, count(t, db, `SELECT count(*) FROM torrents WHERE info_hash = $1`, hash[:]),
		"exact digest-only bans must stop catalogue writes")

	record, err := ledger.Get(t.Context(), moderation.IdentifierForInfoHash(hash))
	require.NoError(t, err)
	require.NotNil(t, record.Evidence)
	require.Equal(t, "Synthetic handbook", record.Evidence.Name)
}

// The importer built by the factory, as production builds it, applies the classifier's
// keyword policy, and a record with no file list does not invent one for the evidence.
func TestExchangeImportKeywordDetectionRetainsOnlyObservedMetadata(t *testing.T) {
	t.Parallel()

	db := dbtest.New(t)
	hash := protocol.ID{123}
	ledger := moderation.NewService(db.Gorm)
	cfg := classifier.NewDefaultConfig()
	cfg.Keywords = map[string][]string{"banned": {"synthetic review marker"}}
	cr := classifier.New(classifier.Params{Config: cfg})

	imp, err := importer.New(importer.Params{
		Dao:            lazy.New(func() (*dao.Query, error) { return db.Query, nil }),
		KeywordPolicy:  cr.KeywordPolicy,
		ExchangeConfig: importer.ExchangeConfig{FilesThreshold: 100},
	}).Exchange.Get()
	require.NoError(t, err)

	_, err = imp.Import(t.Context(),
		exchangeFile(`{"infoHash":"`+hashHex(123)+`","name":"Harmless synthetic review marker"}`), alice())
	require.NoError(t, err)

	record, err := ledger.Get(t.Context(), moderation.IdentifierForInfoHash(hash))
	require.NoError(t, err)
	require.Equal(t, moderation.Pending, record.Status)
	require.Equal(t, "Harmless synthetic review marker", record.Evidence.Name)
	require.Nil(t, record.Evidence.Files, "a record without a file list must not invent a filename")
}
