package dhtcrawler

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/bloom"
	"github.com/bitmagnet-io/bitmagnet/internal/concurrency"
	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/client"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/dht/ktable"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo/banning"
	"github.com/bitmagnet-io/bitmagnet/internal/protocol/metainfo/metainforequester"
	"github.com/prometheus/client_golang/prometheus"
	boom "github.com/tylertreat/BoomFilters"
	"go.uber.org/zap"
)

type crawler struct {
	kTable                       ktable.Table
	client                       client.Client
	metainfoRequester            metainforequester.Requester
	metadataRequestSlots         chan struct{} // bounds peer connections across all in-flight hashes
	banningChecker               banning.Checker
	bootstrapNodes               []string
	reseedBootstrapNodesInterval time.Duration
	reseedRequests               chan struct{}
	getOldestNodesInterval       time.Duration
	oldPeerThreshold             time.Duration
	discoveredNodes              concurrency.BatchingChannel[ktable.Node]
	nodesForPing                 concurrency.BufferedConcurrentChannel[ktable.Node]
	nodesForFindNode             concurrency.BufferedConcurrentChannel[ktable.Node]
	nodesForSampleInfoHashes     concurrency.BufferedConcurrentChannel[ktable.Node]
	infoHashTriage               concurrency.BatchingChannel[nodeHasPeersForHash]
	getPeers                     concurrency.BufferedConcurrentChannel[nodeHasPeersForHash]
	scrape                       concurrency.BufferedConcurrentChannel[nodeHasPeersForHash]
	requestMetaInfo              concurrency.BufferedConcurrentChannel[infoHashWithPeers]
	persistTorrents              concurrency.BatchingChannel[infoHashWithMetaInfo]
	persistSources               concurrency.BatchingChannel[infoHashWithScrape]
	rescrapeThreshold            time.Duration
	saveFilesThreshold           uint
	savePieces                   bool
	dao                          *dao.Query
	keywordPolicy                *moderation.KeywordPolicy
	// ignoreHashes is a thread-safe bloom filter that the crawler keeps in memory,
	// containing every hash it has already encountered.
	// This avoids multiple attempts to crawl the same hash, and takes a lot of load off the database query
	// that checks if a hash has already been indexed.
	ignoreHashes *ignoreHashes
	// soughtNodeID is a random node ID used as the target for find_node and sample_infohashes requests.
	// It is rotated every 10 seconds.
	soughtNodeID   *concurrency.AtomicValue[protocol.ID]
	persistedTotal *prometheus.CounterVec
	logger         *zap.SugaredLogger
	maxQueueDepth  uint
	queueDepth     *concurrency.AtomicValue[int64]
}

// start runs the crawler until ctx is cancelled, and returns only once every
// pipeline stage has stopped and the last batches have been written. The stop
// hook waits for it, so anything started here has to observe the context.
func (c *crawler) start(ctx context.Context) {
	var stages sync.WaitGroup

	run := func(stage func(context.Context)) {
		stages.Add(1)

		go func() {
			defer stages.Done()

			stage(ctx)
		}()
	}

	run(c.rotateSoughtNodeID)
	run(c.runDiscoveredNodes)
	run(c.runPing)
	run(c.runFindNode)
	run(c.getNodesForFindNode)
	run(c.runSampleInfoHashes)
	run(c.getNodesForSampleInfoHashes)
	run(c.runInfoHashTriage)
	run(c.runGetPeers)
	run(c.runRequestMetaInfo)
	run(c.runScrape)
	run(c.reseedBootstrapNodes)
	run(c.runKTableHealthMonitor)
	run(c.runQueueDepthMonitor)
	run(c.runPersistTorrents)
	run(c.runPersistSources)
	run(c.getOldNodes)

	stages.Wait()

	// Everything upstream has stopped, so what is left in the persist channels is
	// all there is ever going to be. It gets a context of its own because ctx is
	// cancelled by now, and a deadline because a stop that never ends is the
	// failure this replaced.
	drainCtx, cancelDrain := context.WithTimeout(context.WithoutCancel(ctx), persistDrainTimeout)
	defer cancelDrain()

	c.drainPersistTorrents(drainCtx)
	c.drainPersistSources(drainCtx)
}

// persistDrainTimeout bounds the final write. It is deliberately well inside
// fx's own StopTimeout of 15 seconds, so the drain either finishes or gives up
// while the stop hook is still waiting for it rather than being cut off.
const persistDrainTimeout = 10 * time.Second

type nodeHasPeersForHash struct {
	infoHash protocol.ID
	node     netip.AddrPort
}

type infoHashWithMetaInfo struct {
	nodeHasPeersForHash
	metaInfo metainfo.Info
}

type infoHashWithPeers struct {
	nodeHasPeersForHash
	peers []netip.AddrPort
}

type infoHashWithScrape struct {
	nodeHasPeersForHash
	bfsd bloom.Filter
	bfpe bloom.Filter
	// scrapedAt is when the scrape response arrived: when the counts were observed.
	scrapedAt time.Time
}

type ignoreHashes struct {
	mutex sync.Mutex
	bloom *boom.StableBloomFilter
}

func (i *ignoreHashes) testAndAdd(id protocol.ID) bool {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	return i.bloom.TestAndAdd(id[:])
}

func (c *crawler) rotateSoughtNodeID(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
			c.soughtNodeID.Set(protocol.RandomNodeID())
		}
	}
}
