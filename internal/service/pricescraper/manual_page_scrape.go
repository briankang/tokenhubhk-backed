package pricescraper

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"go.uber.org/zap"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

// ManualPageScrapeResult is a focused preview used by the new-model wizard.
// It does not write to the database; callers decide which fields to apply.
type ManualPageScrapeResult struct {
	SourceURL   string         `json:"source_url"`
	SupplierID  uint           `json:"supplier_id,omitempty"`
	Supplier    string         `json:"supplier,omitempty"`
	ModelName   string         `json:"model_name,omitempty"`
	Matched     *ScrapedModel  `json:"matched,omitempty"`
	Candidates  []ScrapedModel `json:"candidates"`
	FetchedAt   time.Time      `json:"fetched_at"`
	MatchStatus string         `json:"match_status"`
	Warnings    []string       `json:"warnings,omitempty"`
}

// ScrapeModelFromPage fetches a single official pricing page and extracts a model
// price preview for the wizard.
func (s *PriceScraperService) ScrapeModelFromPage(ctx context.Context, supplierID uint, pageURL, modelName, typeHint string) (*ManualPageScrapeResult, error) {
	pageURL = strings.TrimSpace(pageURL)
	if pageURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(pageURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("only absolute http/https URLs are supported")
	}

	var supplier model.Supplier
	if supplierID > 0 && s.db != nil {
		if err := s.db.First(&supplier, supplierID).Error; err != nil {
			return nil, fmt.Errorf("load supplier: %w", err)
		}
	}

	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}
	log.Info("manual model pricing page scrape", zap.String("url", pageURL), zap.String("model", modelName))

	if s.browserMgr == nil {
		s.browserMgr = NewBrowserManager()
	}
	html, err := s.browserMgr.FetchRenderedHTML(ctx, pageURL)
	if err != nil {
		return nil, err
	}

	result, err := ExtractModelPricingFromHTML(html, modelName, typeHint)
	if err != nil {
		return nil, err
	}
	result.SourceURL = pageURL
	result.SupplierID = supplierID
	result.Supplier = supplier.Name
	return result, nil
}

// ExtractModelPricingFromHTML parses an HTML pricing page. It is kept separate
// so tests can cover the extraction logic without launching a browser.
func ExtractModelPricingFromHTML(html, modelName, typeHint string) (*ManualPageScrapeResult, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}
	models := extractPriceTables(doc)
	if strings.TrimSpace(typeHint) != "" {
		for i := range models {
			if models[i].ModelType == "" || models[i].ModelType == "LLM" {
				models[i].ModelType = strings.TrimSpace(typeHint)
			}
		}
	}
	models = dedupeAcrossPages(models)
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i].ModelName) < strings.ToLower(models[j].ModelName)
	})

	result := &ManualPageScrapeResult{
		ModelName:   strings.TrimSpace(modelName),
		Candidates:  limitScrapedCandidates(models, 30),
		FetchedAt:   time.Now(),
		MatchStatus: "no_model_name",
	}
	if len(models) == 0 {
		result.MatchStatus = "no_price_table"
		result.Warnings = append(result.Warnings, "No recognizable pricing table was found on this page.")
		return result, nil
	}
	if strings.TrimSpace(modelName) == "" {
		return result, nil
	}
	if match := FuzzyMatchModel(modelName, models); match != nil {
		cp := *match
		result.Matched = &cp
		result.MatchStatus = "matched"
		return result, nil
	}
	result.MatchStatus = "not_found"
	result.Warnings = append(result.Warnings, "The page was parsed, but no row matched the requested model name.")
	return result, nil
}

func limitScrapedCandidates(items []ScrapedModel, max int) []ScrapedModel {
	if len(items) <= max {
		return items
	}
	return items[:max]
}
