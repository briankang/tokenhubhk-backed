package pricescraper

import "testing"

func TestExtractModelPricingFromHTMLMatchesRequestedModel(t *testing.T) {
	html := `
		<html><body>
			<table>
				<thead>
					<tr><th>Model</th><th>Input price</th><th>Output price</th></tr>
				</thead>
				<tbody>
					<tr><td>demo-basic</td><td>1.00</td><td>2.00</td></tr>
					<tr><td>qwen-plus</td><td>4.00</td><td>12.00</td></tr>
				</tbody>
			</table>
		</body></html>`

	result, err := ExtractModelPricingFromHTML(html, "qwen-plus", "LLM")
	if err != nil {
		t.Fatalf("ExtractModelPricingFromHTML returned error: %v", err)
	}
	if result.MatchStatus != "matched" {
		t.Fatalf("MatchStatus=%q, want matched", result.MatchStatus)
	}
	if result.Matched == nil {
		t.Fatal("Matched is nil")
	}
	if result.Matched.ModelName != "qwen-plus" {
		t.Fatalf("Matched.ModelName=%q, want qwen-plus", result.Matched.ModelName)
	}
	if result.Matched.InputPrice != 4 || result.Matched.OutputPrice != 12 {
		t.Fatalf("matched prices=(%v,%v), want (4,12)", result.Matched.InputPrice, result.Matched.OutputPrice)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("Candidates len=%d, want 2", len(result.Candidates))
	}
}

func TestExtractModelPricingFromHTMLReportsNoPriceTable(t *testing.T) {
	result, err := ExtractModelPricingFromHTML(`<html><body><p>No table here</p></body></html>`, "qwen-plus", "LLM")
	if err != nil {
		t.Fatalf("ExtractModelPricingFromHTML returned error: %v", err)
	}
	if result.MatchStatus != "no_price_table" {
		t.Fatalf("MatchStatus=%q, want no_price_table", result.MatchStatus)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected warning for missing pricing table")
	}
}
