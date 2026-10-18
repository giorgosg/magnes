package importerfx

import (
	"github.com/bitmagnet-io/bitmagnet/internal/dhtcrawler"
	"github.com/bitmagnet-io/bitmagnet/internal/importer"
	"github.com/bitmagnet-io/bitmagnet/internal/importer/httpserver"
	"go.uber.org/fx"
)

func New() fx.Option {
	return fx.Module(
		"importer",
		fx.Provide(
			httpserver.New,
			importer.New,
			exchangeConfig,
		),
	)
}

// exchangeConfig stores imported file lists under the crawler's own threshold, so an
// imported torrent is stored as a crawled one would be. It lives here rather than in
// package importer so that the importer does not depend on the crawler.
func exchangeConfig(crawler dhtcrawler.Config) importer.ExchangeConfig {
	return importer.ExchangeConfig{FilesThreshold: crawler.SaveFilesThreshold}
}
