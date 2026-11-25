package documents

import (
	"context"
	"errors"

	"mar-backend/internal/storage"
)

type Result struct {
	ResourceID string  `json:"resource_id"`
	FileName   string  `json:"file_name"`
	FragmentID string  `json:"fragment_id"`
	Text       string  `json:"text"`
	Score      float32 `json:"score"`
	Page       int     `json:"page,omitempty"`
	Pages      []int   `json:"pages,omitempty"`
	Section    string  `json:"section,omitempty"`
	Sheet      string  `json:"sheet,omitempty"`
	Cells      string  `json:"cells,omitempty"`
}

func (p *Pipeline) Search(ctx context.Context, tenant, query, resourceID string, limit int) ([]Result, error) {
	vectors, err := p.Embed.Embed(ctx, []string{query})
	if err != nil {
		return nil, failed("embedding_unavailable", err)
	}
	if err = validateVectors(vectors, 1); err != nil {
		return nil, failed("embedding_response_invalid", err)
	}
	filter := storage.TenantFilter(tenant, storage.FieldMatch{Field: storage.FieldIndexVersion, Value: p.IndexVersion()})
	if resourceID != "" {
		filter.Must = append(filter.Must, storage.FieldMatch{Field: storage.FieldResourceID, Value: resourceID})
	}
	results := make([]Result, 0, limit)
	resources := make(map[string]storage.Resource)
	const batch = 64
	for offset := uint64(0); ; offset += batch {
		hits, err := p.Vectors.Search(ctx, p.Collection, storage.SearchQuery{
			VectorName: "dense", Vector: vectors[0], Filter: filter, Limit: batch, Offset: offset, WithPayload: true,
		})
		if err != nil {
			return nil, failed("index_unavailable", err)
		}
		for _, hit := range hits {
			id, _ := hit.Payload[storage.FieldResourceID].(string)
			if id == "" {
				continue
			}
			resource, checked := resources[id]
			if !checked {
				resource, err = p.Resources.Get(ctx, tenant, id)
				if err != nil && !errors.Is(err, storage.ErrNotFound) {
					return nil, failed("resource_unavailable", err)
				}
				resources[id] = resource
			}
			if resource.Status != storage.ResourceProcessed || resource.IndexVersion != p.IndexVersion() {
				continue
			}
			text, _ := hit.Payload["text"].(string)
			section, _ := hit.Payload[storage.FieldSection].(string)
			sheet, _ := hit.Payload["sheet"].(string)
			cells, _ := hit.Payload["cells"].(string)
			var page int
			switch value := hit.Payload[storage.FieldPageNumber].(type) {
			case int64:
				page = int(value)
			case int:
				page = value
			}
			var pages []int
			if values, ok := hit.Payload["pages"].([]any); ok {
				for _, value := range values {
					if number, ok := value.(int64); ok {
						pages = append(pages, int(number))
					}
				}
			}
			results = append(results, Result{ResourceID: id, FileName: resource.FileName, FragmentID: hit.ID,
				Text: text, Score: hit.Score, Page: page, Pages: pages, Section: section, Sheet: sheet, Cells: cells})
			if len(results) == limit {
				return results, nil
			}
		}
		if len(hits) < batch {
			return results, nil
		}
	}
}
