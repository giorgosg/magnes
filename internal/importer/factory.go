package importer

import (
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
	}
}
