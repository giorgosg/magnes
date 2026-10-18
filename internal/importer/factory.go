package importer

import (
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/database/dao"
	"github.com/bitmagnet-io/bitmagnet/internal/lazy"
	"github.com/bitmagnet-io/bitmagnet/internal/moderation"
	"go.uber.org/fx"
)

type Params struct {
	fx.In
	Dao            lazy.Lazy[*dao.Query]
	KeywordPolicy  lazy.Lazy[*moderation.KeywordPolicy]
	ExchangeConfig ExchangeConfig
}

type Result struct {
	fx.Out
	Importer lazy.Lazy[Importer]
	Exchange lazy.Lazy[*ExchangeImporter]
}

func New(p Params) Result {
	return Result{
		Exchange: lazy.New(func() (*ExchangeImporter, error) {
			d, err := p.Dao.Get()
			if err != nil {
				return nil, err
			}

			var policy *moderation.KeywordPolicy
			if p.KeywordPolicy != nil {
				if policy, err = p.KeywordPolicy.Get(); err != nil {
					return nil, err
				}
			}

			return NewExchangeImporter(d.UnderlyingDB(), policy, p.ExchangeConfig), nil
		}),
		Importer: lazy.New(func() (Importer, error) {
			d, err := p.Dao.Get()
			if err != nil {
				return nil, err
			}

			var policy *moderation.KeywordPolicy
			if p.KeywordPolicy != nil {
				policy, err = p.KeywordPolicy.Get()
				if err != nil {
					return nil, err
				}
			}

			return importer{
				keywordPolicy: policy,
				dao:           d,
				bufferSize:    100,
				maxWaitTime:   500 * time.Millisecond,
			}, nil
		}),
	}
}
