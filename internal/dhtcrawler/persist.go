package dhtcrawler

import (
	"context"
	"database/sql/driver"
	"strings"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/model"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/processor"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const dhtSource = "dht"

// runPersistTorrents waits on the persistTorrents channel, and persists torrents to the database in batches.
// After persisting each batch it will publish a message to the classifier,
// and forward the hash on the scrape channel to attempt finding the seeders/leechers.
func (c *crawler) runPersistTorrents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case is := <-c.persistTorrents.Out():
			c.persistTorrentBatch(ctx, is)
		}
	}
}

// persistTorrentBatch writes a batch and then hands the hashes on to be
// scraped for seeders and leechers.
func (c *crawler) persistTorrentBatch(ctx context.Context, is []infoHashWithMetaInfo) {
	hashMap, ok := c.writeTorrentBatch(ctx, is)
	if !ok {
		return
	}

	for _, i := range hashMap {
		select {
		case <-ctx.Done():
			return
		case c.scrape.In() <- i.nodeHasPeersForHash:
		}
	}
}

// writeTorrentBatch persists a batch and reports what it wrote. The forwarding
// is deliberately not part of it: the shutdown drain calls this directly, at a
// point where the scrape stage has already stopped and a hand-off would block
// on a channel nothing is reading.
func (c *crawler) writeTorrentBatch(
	ctx context.Context,
	is []infoHashWithMetaInfo,
) (map[protocol.ID]infoHashWithMetaInfo, bool) {
	evidence := make([]moderation.Evidence, 0, len(is))
	for _, item := range is {
		hash := item.infoHash

		files := make([]string, 0, len(item.metaInfo.Files))
		for _, file := range item.metaInfo.Files {
			files = append(files, file.DisplayPath(&item.metaInfo))
		}

		if len(files) == 0 {
			files = append(files, item.metaInfo.BestName())
		}

		evidence = append(
			evidence,
			moderation.Evidence{InfoHash: &hash, Name: item.metaInfo.BestName(), Files: files},
		)
	}

	var written map[protocol.ID]infoHashWithMetaInfo

	ledger := moderation.NewService(c.dao.UnderlyingDB())
	for _, observed := range evidence {
		if _, err := c.keywordPolicy.Apply(ctx, ledger, observed); err != nil {
			c.logger.Errorf("error checking harmful keywords: %s", err)
			return nil, false
		}
	}

	err := ledger.Ingest(ctx, evidence,
		func(db *gorm.DB, permitted map[protocol.ID]bool) error {
			eligible := make([]infoHashWithMetaInfo, 0, len(is))
			for _, item := range is {
				if permitted[item.infoHash] {
					eligible = append(eligible, item)
				}
			}

			if len(eligible) == 0 {
				written = map[protocol.ID]infoHashWithMetaInfo{}
				return nil
			}

			var err error

			written, err = c.writeEligibleTorrentBatch(ctx, eligible, dao.Use(db))

			return err
		})
	if err != nil {
		c.logger.Errorf("error persisting torrents: %s", err)
		return nil, false
	}

	c.persistedTotal.With(prometheus.Labels{labelEntity: "Torrent"}).Add(float64(len(written)))

	return written, true
}

func (c *crawler) writeEligibleTorrentBatch(
	ctx context.Context,
	is []infoHashWithMetaInfo,
	tx *dao.Query,
) (map[protocol.ID]infoHashWithMetaInfo, error) {
	torrentsToPersist := make([]*model.Torrent, 0, len(is))

	var torrentFilesToPersist []*model.TorrentFile

	var torrentSourcesToPersist []*model.TorrentsTorrentSource

	var torrentPiecesToPersist []*model.TorrentPieces

	var queueJobsToPersist []*model.QueueJob

	hashMap := make(map[protocol.ID]infoHashWithMetaInfo, len(is))

	var hashesToClassify []protocol.ID

	flushHashesToClassify := func() {
		if len(hashesToClassify) > 0 {
			// Adapted from o51r15/bitmagnet@f7cb97d4. Torrents are still
			// persisted when the classification queue is saturated; a later
			// reprocess can pick them up after the queue drains.
			if c.classificationQueueAtCapacity() {
				c.logger.Debugw("queue depth threshold reached, skipping classification",
					"depth", c.queueDepth.Get(),
					"max", c.maxQueueDepth,
				)
			} else if job, err := processor.NewQueueJob(processor.MessageParams{
				InfoHashes: hashesToClassify,
			}, model.QueueJobDelayBy(time.Minute)); err != nil {
				c.logger.Errorf("error creating queue job: %s", err.Error())
			} else {
				queueJobsToPersist = append(queueJobsToPersist, &job)
			}
		}

		hashesToClassify = make([]protocol.ID, 0, classifyBatchSize)
	}
	flushHashesToClassify()

	for _, i := range is {
		if _, ok := hashMap[i.infoHash]; ok {
			continue
		}

		hashMap[i.infoHash] = i

		if t, err := createTorrentModel(
			i.infoHash,
			i.metaInfo,
			c.savePieces,
			c.saveFilesThreshold,
		); err != nil {
			c.logger.Errorf("error creating torrent model: %s", err.Error())
		} else {
			for _, f := range t.Files {
				fc := f
				torrentFilesToPersist = append(torrentFilesToPersist, &fc)
			}

			t.Files = nil

			for _, s := range t.Sources {
				sc := s
				torrentSourcesToPersist = append(torrentSourcesToPersist, &sc)
			}

			t.Sources = nil

			if c.savePieces {
				pc := t.Pieces
				torrentPiecesToPersist = append(torrentPiecesToPersist, &pc)

				t.Pieces = model.TorrentPieces{}
			}

			torrentsToPersist = append(torrentsToPersist, &t)

			hashesToClassify = append(hashesToClassify, i.infoHash)

			if len(hashesToClassify) >= classifyBatchSize {
				flushHashesToClassify()
			}
		}
	}

	flushHashesToClassify()

	if persistErr := func() error {
		if err := tx.WithContext(ctx).Torrent.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: string(c.dao.Torrent.InfoHash.ColumnName())}},
			DoUpdates: clause.AssignmentColumns([]string{
				string(c.dao.Torrent.Name.ColumnName()),
				string(c.dao.Torrent.FilesStatus.ColumnName()),
				string(c.dao.Torrent.FilesCount.ColumnName()),
				string(c.dao.Torrent.UpdatedAt.ColumnName()),
			}),
		}).CreateInBatches(torrentsToPersist, 100); err != nil {
			return err
		}

		if len(torrentFilesToPersist) > 0 {
			if err := tx.WithContext(ctx).TorrentFile.Clauses(clause.OnConflict{
				DoNothing: true,
			}).CreateInBatches(torrentFilesToPersist, 100); err != nil {
				return err
			}
		}

		if err := tx.WithContext(ctx).TorrentsTorrentSource.Clauses(clause.OnConflict{
			DoNothing: true,
		}).CreateInBatches(torrentSourcesToPersist, 100); err != nil {
			return err
		}

		if c.savePieces {
			if err := tx.WithContext(ctx).TorrentPieces.Clauses(clause.OnConflict{
				DoNothing: true,
			}).CreateInBatches(torrentPiecesToPersist, 10); err != nil {
				return err
			}
		}

		return tx.WithContext(ctx).QueueJob.Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(queueJobsToPersist, 10)
	}(); persistErr != nil {
		return nil, persistErr
	}

	c.logger.Debugw("persisted torrents", "count", len(torrentsToPersist))

	return hashMap, nil
}

// drainPersistTorrents writes what the torrent pipeline still holds. It runs
// once, from start, after every stage that could feed the channel has returned -
// so closing the channel cannot strand a sender, and there is no scrape stage
// left to forward to.
func (c *crawler) drainPersistTorrents(ctx context.Context) {
	c.persistTorrents.Close()

	for is := range c.persistTorrents.Out() {
		c.writeTorrentBatch(ctx, is)
	}
}

// drainPersistSources does the same for the scraped seeder and leecher counts.
func (c *crawler) drainPersistSources(ctx context.Context) {
	c.persistSources.Close()

	for scrapes := range c.persistSources.Out() {
		c.persistSourceBatch(ctx, scrapes)
	}
}

// isPaddingFile reports whether a file entry is a BEP-47 padding file, which
// exists to align the following file with a piece boundary and is not content.
func isPaddingFile(displayPath string) bool {
	return strings.HasPrefix(displayPath, ".pad/")
}

func createTorrentModel(
	hash protocol.ID,
	info metainfo.Info,
	savePieces bool,
	saveFilesThreshold uint,
) (model.Torrent, error) {
	name := info.BestName()

	private := false
	if info.Private != nil {
		private = *info.Private
	}

	var filesCount model.NullUint

	filesStatus := model.FilesStatusSingle
	if len(info.Files) > 0 {
		filesStatus = model.FilesStatusMulti
		// Count what the database will hold, not what the torrent declares: the
		// padding entries skipped below are never written as torrent_files rows,
		// so counting them makes files_count disagree with the rows it describes.
		//
		// This is the torrent's full non-padding count, not the number of rows
		// actually written - over the threshold those differ, and infohash_triage
		// re-fetches metainfo whenever files_status is over_threshold and
		// files_count <= saveFilesThreshold. A capped count would loop forever.
		nonPaddingCount := 0

		for _, file := range info.Files {
			if !isPaddingFile(file.DisplayPath(&info)) {
				nonPaddingCount++
			}
		}

		filesCount = model.NewNullUint(uint(nonPaddingCount))
	}

	files := make([]model.TorrentFile, 0, min(int(saveFilesThreshold), len(info.Files)))

	// Adapted from o51r15/bitmagnet@5efcbd1c5: count only persisted files;
	// BEP-47 padding entries are synthetic and do not belong in the database.
	savedCount := 0

	for i, file := range info.Files {
		displayPath := file.DisplayPath(&info)
		if isPaddingFile(displayPath) {
			continue
		}

		if savedCount >= int(saveFilesThreshold) {
			filesStatus = model.FilesStatusOverThreshold
			break
		}

		files = append(files, model.TorrentFile{
			InfoHash: hash,
			Index:    uint(i),
			Path:     displayPath,
			Size:     uint(file.Length),
		})
		savedCount++
	}

	var pieces model.TorrentPieces
	if savePieces {
		pieces = model.TorrentPieces{
			InfoHash:    hash,
			PieceLength: info.PieceLength,
			Pieces:      info.Pieces,
		}
	}

	return model.Torrent{
		InfoHash:    hash,
		Name:        name,
		Size:        uint(info.TotalLength()),
		Private:     private,
		Pieces:      pieces,
		Files:       files,
		FilesStatus: filesStatus,
		FilesCount:  filesCount,
		Sources: []model.TorrentsTorrentSource{
			{
				Source:   dhtSource,
				InfoHash: hash,
			},
		},
	}, nil
}

const classifyBatchSize = 100

const queueDepthCheckInterval = 30 * time.Second

func (c *crawler) classificationQueueAtCapacity() bool {
	return c.maxQueueDepth > 0 && c.queueDepth.Get() >= int64(c.maxQueueDepth)
}

func (c *crawler) runQueueDepthMonitor(ctx context.Context) {
	if c.maxQueueDepth == 0 {
		return
	}

	if err := c.refreshQueueDepth(ctx); err != nil {
		c.logger.Debugw("queue depth monitor: initial count failed", "error", err)
	}

	ticker := time.NewTicker(queueDepthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.refreshQueueDepth(ctx); err != nil {
				c.logger.Debugw("queue depth monitor: count failed", "error", err)
			}
		}
	}
}

func (c *crawler) refreshQueueDepth(ctx context.Context) error {
	count, err := c.dao.WithContext(ctx).QueueJob.Where(
		c.dao.QueueJob.Status.In(
			string(model.QueueJobStatusPending),
			string(model.QueueJobStatusRetry),
		),
	).Count()
	if err != nil {
		return err
	}

	c.queueDepth.Set(count)

	return nil
}

// runPersistSources waits on the persistSources channel for scraped torrents, and persists sources
// (which includes discovery date, seeders and leechers) to the database in batches.
func (c *crawler) runPersistSources(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case scrapes := <-c.persistSources.Out():
			c.persistSourceBatch(ctx, scrapes)
		}
	}
}

func (c *crawler) persistSourceBatch(ctx context.Context, scrapes []infoHashWithScrape) {
	evidence := make([]moderation.Evidence, 0, len(scrapes))
	for _, scrape := range scrapes {
		hash := scrape.infoHash
		evidence = append(evidence, moderation.Evidence{InfoHash: &hash})
	}

	var count int

	err := moderation.NewService(c.dao.UnderlyingDB()).Ingest(ctx, evidence,
		func(db *gorm.DB, permitted map[protocol.ID]bool) error {
			eligible := make([]infoHashWithScrape, 0, len(scrapes))
			for _, scrape := range scrapes {
				if permitted[scrape.infoHash] {
					eligible = append(eligible, scrape)
				}
			}

			if len(eligible) == 0 {
				return nil
			}

			var err error

			count, err = c.writeEligibleSourceBatch(ctx, eligible, dao.Use(db))

			return err
		})
	if err != nil {
		c.logger.Errorf("error persisting torrent sources: %s", err)
		return
	}

	c.persistedTotal.With(prometheus.Labels{labelEntity: "TorrentsTorrentSource"}).Add(float64(count))
	c.logger.Debugw("persisted torrent sources", "count", count)
}

func (c *crawler) writeEligibleSourceBatch(
	ctx context.Context, scrapes []infoHashWithScrape, tx *dao.Query,
) (int, error) {
	hashes := make([]driver.Valuer, 0, len(scrapes))

	hashSet := make(map[protocol.ID]struct{}, len(scrapes))
	for _, s := range scrapes {
		if _, ok := hashSet[s.infoHash]; ok {
			continue
		}

		hashSet[s.infoHash] = struct{}{}
		hashes = append(hashes, s.infoHash)
	}

	// A torrent deleted since triage would fail the foreign key and reject the whole
	// batch, so skip it here. KEY SHARE is the lock the foreign key check takes
	// anyway; taking it first stops a delete committing before the insert.
	var existing []protocol.ID
	// Raw because GORM's locking clause accepts only UPDATE and SHARE, and SHARE
	// would also hold off concurrent updates to these torrents.
	if err := tx.Torrent.WithContext(ctx).UnderlyingDB().
		Raw("SELECT info_hash FROM torrents WHERE info_hash IN ? ORDER BY info_hash FOR KEY SHARE", hashes).
		Scan(&existing).Error; err != nil {
		return 0, err
	}

	exists := make(map[protocol.ID]struct{}, len(existing))
	for _, hash := range existing {
		exists[hash] = struct{}{}
	}

	srcs := make([]*model.TorrentsTorrentSource, 0, len(existing))

	for _, s := range scrapes {
		if _, ok := exists[s.infoHash]; !ok {
			continue
		}

		delete(exists, s.infoHash)

		if src, err := createTorrentSourceModel(s); err != nil {
			c.logger.Errorf("error creating torrent source model: %s", err.Error())
		} else {
			srcs = append(srcs, &src)
		}
	}

	if len(srcs) == 0 {
		return 0, nil
	}

	persistErr := tx.WithContext(ctx).TorrentsTorrentSource.Clauses(
		clause.OnConflict{
			Columns: []clause.Column{
				{Name: string(c.dao.TorrentsTorrentSource.InfoHash.ColumnName())},
				{Name: string(c.dao.TorrentsTorrentSource.Source.ColumnName())},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				string(c.dao.TorrentsTorrentSource.Seeders.ColumnName()),
				string(c.dao.TorrentsTorrentSource.Leechers.ColumnName()),
				// sets to null, fixes torrents indexed before 0.8.0 with published_at
				// 0001-01-01 00:00:00+00:
				string(c.dao.TorrentsTorrentSource.PublishedAt.ColumnName()),
				string(c.dao.TorrentsTorrentSource.UpdatedAt.ColumnName()),
			}),
		},
	).CreateInBatches(srcs, 100)

	return len(srcs), persistErr
}

func createTorrentSourceModel(
	result infoHashWithScrape,
) (model.TorrentsTorrentSource, error) {
	seeders := model.NewNullUint(uint(result.bfsd.ApproximatedSize()))
	leechers := model.NewNullUint(uint(result.bfpe.ApproximatedSize()))

	return model.TorrentsTorrentSource{
		Source:   dhtSource,
		InfoHash: result.infoHash,
		Seeders:  seeders,
		Leechers: leechers,
	}, nil
}
