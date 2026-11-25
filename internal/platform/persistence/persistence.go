package persistence

import (
	"fmt"

	"mar-backend/internal/platform/config"
	"mar-backend/internal/storage/blob"
	"mar-backend/internal/storage/table"
)

func New(cfg config.Config) (*blob.Store, *table.Store, error) {
	objects, err := blob.New(blob.Config{
		ConnectionString: cfg.Blob.ConnectionString,
		Endpoint:         cfg.Blob.Endpoint,
		AccountName:      cfg.Blob.AccountName,
		AccountKey:       cfg.Blob.AccountKey,
		RequestTimeout:   cfg.Blob.RequestTimeout,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("almacenamiento de objetos: %w", err)
	}
	tables, err := table.New(table.Config{
		ConnectionString: cfg.Table.ConnectionString,
		Endpoint:         cfg.Table.Endpoint,
		AccountName:      cfg.Table.AccountName,
		AccountKey:       cfg.Table.AccountKey,
		TableResources:   cfg.Table.TableResources,
		TableJobs:        cfg.Table.TableJobs,
		RequestTimeout:   cfg.Table.RequestTimeout,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("almacenamiento tabular: %w", err)
	}
	return objects, tables, nil
}
