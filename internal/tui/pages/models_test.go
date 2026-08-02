package pages

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
)

func TestRenderModelsKeepsDenseDeskTiersAligned(t *testing.T) {
	data := modelsTestData()
	cases := []struct {
		name   string
		width  int
		height int
		view   ModelsViewport
		want   []string
	}{
		{
			name: "wide tall", width: 168, height: 59,
			view: ModelsViewport{Width: 168, Height: 59, Wide: true, Tall: true},
			want: []string{"TOK%", "COST CONCENTRATION", "PROVIDER ROLLUP", "PRICING PROVENANCE", "TOKENS PER SESSION", "UNPRICED MODELS", "RECENCY", "MODEL × DAY", "30-DAY COST"},
		},
		{
			name: "standard", width: 96, height: 33,
			view: ModelsViewport{Width: 96, Height: 33, Standard: true},
			want: []string{"PROVIDER", "COST PER 1M TOKENS", "RECENCY"},
		},
		{
			name: "floor", width: 78, height: 18,
			view: ModelsViewport{Width: 78, Height: 18},
			want: []string{"PROVIDER", "MODEL", "TOKENS", "COST"},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			view := RenderModels(testRender(), data, test.view)
			lines := strings.Split(view, "\n")
			if len(lines) != test.height {
				t.Fatalf("line count = %d, want %d\n%s", len(lines), test.height, view)
			}
			for index, line := range lines {
				if got := lipgloss.Width(line); got != test.width {
					t.Fatalf("line %d width = %d, want %d\n%s", index+1, got, test.width, view)
				}
			}
			for _, fragment := range test.want {
				if !strings.Contains(view, fragment) {
					t.Errorf("view missing %q:\n%s", fragment, view)
				}
			}
			if test.view.Wide && test.view.Tall {
				for index, line := range lines {
					if strings.Trim(line, "·") == "" {
						t.Fatalf("wide+tall view has a stamped filler row at line %d:\n%s", index+1, view)
					}
				}
			}
		})
	}
}

func TestRenderModelsDropsSparklineBelowWideContentWidth(t *testing.T) {
	data := modelsTestData()
	wide := RenderModels(testRender(), data, ModelsViewport{Width: 168, Height: 59, Wide: true, Tall: true})
	standard := RenderModels(testRender(), data, ModelsViewport{Width: 96, Height: 33, Standard: true})
	if !strings.Contains(wide, "COST 30D") || !strings.Contains(wide, "▃") {
		t.Fatalf("wide master table did not render the sparkline column:\n%s", wide)
	}
	if strings.Contains(standard, "COST 30D") || strings.Contains(standard, "▃") {
		t.Fatalf("standard master table retained the sparkline column:\n%s", standard)
	}
}

func TestRenderModelsUsesSessionDenominatorWhenProvided(t *testing.T) {
	data := modelsTestData()
	data.Rows = data.Rows[:1]
	data.Rows[0].Sessions = 4
	data.PerSession = []ModelPerSessionRow{{Model: data.Rows[0].Model, Tokens: data.Rows[0].Tokens, Sessions: 4, TokensPerSession: data.Rows[0].Tokens / 4}}
	view := RenderModels(testRender(), data, ModelsViewport{Width: 168, Height: 59, Wide: true, Tall: true})
	if !strings.Contains(view, "TOKENS PER SESSION") || !strings.Contains(view, "25.0M") {
		t.Fatalf("session analysis did not use the supplied denominator:\n%s", view)
	}
}

func TestModelsButterflyLabelsRankColumn(t *testing.T) {
	view := strings.Join(modelTokenCostLines(testRender(), modelsTestData(), 69), "\n")
	if !strings.Contains(view, "RANK") || !strings.Contains(view, "#1") {
		t.Fatalf("butterfly rank column is not labeled:\n%s", view)
	}
}

func TestModelsRatesRespectAllocatedHeight(t *testing.T) {
	data := modelsTestData()
	for _, height := range []int{6, 13, 23} {
		lines := modelRatesLines(testRender(), data, height)
		if len(lines) > height {
			t.Fatalf("rates pane returned %d lines for content height %d:\n%s", len(lines), height, strings.Join(lines, "\n"))
		}
	}
	view := strings.Join(modelRatesLines(testRender(), data, 13), "\n")
	for _, section := range []string{"UNPRICED MODELS", "RECENCY"} {
		if !strings.Contains(view, section) {
			t.Fatalf("rates pane dropped the %s section despite reserved height:\n%s", section, view)
		}
	}
}

func TestModelsMasterColumnsKeepTotalsInsideTheirColumns(t *testing.T) {
	data := modelsTestData()
	data.Total.TokenShare = 1.2
	data.Total.CostShare = 1.2
	view := RenderModels(testRender(), data, ModelsViewport{Width: 168, Height: 59, Wide: true, Tall: true})
	var header, row, total string
	for _, line := range strings.Split(view, "\n") {
		switch {
		case header == "" && strings.Contains(line, "PROV   MODEL"):
			header = line
		case row == "" && strings.Contains(line, "Codex") && strings.Contains(line, "model-00"):
			row = line
		case total == "" && strings.Contains(line, "TOTAL") && strings.Contains(line, "models"):
			total = line
		}
	}
	if header == "" || row == "" || total == "" {
		t.Fatalf("could not find master table header, row, and total:\n%s", view)
	}
	if got, want := strings.Index(header, "PROV"), strings.Index(row, "Codex"); got != want {
		t.Fatalf("master header starts at %d, row starts at %d:\nheader=%s\nrow=%s", got, want, header, row)
	}
	if !strings.Contains(total, "10 priced") || strings.Contains(total, "10 rate") {
		t.Fatalf("total pricing label is wrong:\n%s", total)
	}
	if strings.Contains(total, "120.0") || strings.Contains(total, "120.00") || !strings.Contains(total, "100.0") {
		t.Fatalf("total share overflowed its columns:\n%s", total)
	}
}

func TestModelsAnalysisUsesCumulativeCostShare(t *testing.T) {
	lines := modelTokenCostLines(testRender(), modelsTestData(), 69)
	view := strings.Join(lines, "\n")
	if !strings.Contains(view, "top 2  model-08") || !strings.Contains(view, "34.5") {
		t.Fatalf("cost concentration is not cumulative:\n%s", view)
	}
}

func TestModelsRollupsMarkPartialCosts(t *testing.T) {
	data := ModelPageData{
		Providers: []ModelProviderRow{{Provider: "codex", Models: 1, Tokens: 100, Cost: pricing.Money(1_000_000_000), PricedTokens: 60, UnpricedTokens: 40}},
		Pricing:   []ModelPricingRow{{Label: "partial", Models: 1, Tokens: 100, Cost: pricing.Money(1_000_000_000), PricedTokens: 60, UnpricedTokens: 40}},
	}
	view := strings.Join(modelProviderLines(testRender(), data), "\n")
	if strings.Count(view, "~$1.00") != 2 {
		t.Fatalf("rollups did not mark partial costs:\n%s", view)
	}
}

func TestModelsMasterListCapacityFollowsHeight(t *testing.T) {
	data := modelsTestData()
	for index := 10; index < 20; index++ {
		row := data.Rows[index%len(data.Rows)]
		row.Model = fmt.Sprintf("model-%02d", index)
		data.Rows = append(data.Rows, row)
	}
	data.Total.Model = "20 models"

	short := renderModelMaster(testRender(), data, ModelsViewport{Width: 96, Standard: true}, 96, 11, false)
	tall := renderModelMaster(testRender(), data, ModelsViewport{Width: 96, Standard: true}, 96, 13, false)
	if got, want := strings.Count(short, "model-"), 7; got != want {
		t.Fatalf("short master rows = %d, want %d:\n%s", got, want, short)
	}
	if got, want := strings.Count(tall, "model-"), 9; got != want {
		t.Fatalf("tall master rows = %d, want %d:\n%s", got, want, tall)
	}
}

func TestQuest148FrameSnapshots(t *testing.T) {
	data := modelsTestData()
	for _, test := range []struct {
		name string
		view ModelsViewport
	}{
		{name: "wide+tall", view: ModelsViewport{Width: 168, Height: 59, Wide: true, Tall: true}},
		{name: "standard", view: ModelsViewport{Width: 96, Height: 33, Standard: true}},
		{name: "floor", view: ModelsViewport{Width: 78, Height: 18}},
	} {
		t.Logf("FRAME: %s\nSource: internal/tui/pages/models_test.go::TestQuest148FrameSnapshots\nCommand: go test -v ./internal/tui/pages -run TestQuest148FrameSnapshots -count=1\n\n%s", test.name, RenderModels(testRender(), data, test.view))
	}
}

func modelsTestData() ModelPageData {
	data := ModelPageData{ScopeLabel: "ALL TIME"}
	for index := 0; index < 10; index++ {
		provider := "codex"
		if index%2 == 1 {
			provider = "claude"
		}
		tokens := int64(100-index*7) * 1_000_000
		cost := pricing.Money(int64(index+1) * 1_000_000_000)
		row := ModelPageRow{
			Provider: provider, Model: fmt.Sprintf("model-%02d", index), Tokens: tokens,
			Cost: cost, PricedTokens: tokens, TokenShare: float64(tokens) / 650_000_000,
			CostShare: float64(index+1) / 55, Pricing: "live", Sessions: index + 1,
			Days: 30 - index, FirstDate: "2026-07-03", LastDate: "2026-08-01",
		}
		for spark := 0; spark < 10; spark++ {
			value := float64((index + 1) * (spark + 1))
			switch index {
			case 0:
				if spark == 5 {
					value *= 4
				}
			case 1:
				value = float64((index + 1) * (10 - spark))
			case 2:
				if spark == 4 || spark == 5 {
					value = 0
				}
			}
			row.Sparkline = append(row.Sparkline, value)
		}
		data.Rows = append(data.Rows, row)
	}
	data.Total = ModelPageRow{Provider: "TOTAL", Model: "10 models", Tokens: 650_000_000, Cost: pricing.Money(55_000_000_000), PricedTokens: 650_000_000, TokenShare: 1, CostShare: 1, Pricing: "10 priced", Days: 30, FirstDate: "2026-07-03", LastDate: "2026-08-01"}
	data.Providers = []ModelProviderRow{
		{Provider: "codex", Models: 5, Tokens: 350_000_000, Cost: pricing.Money(25_000_000_000), PricedTokens: 350_000_000, TokenShare: .54, CostShare: .45},
		{Provider: "claude", Models: 5, Tokens: 300_000_000, Cost: pricing.Money(30_000_000_000), PricedTokens: 300_000_000, TokenShare: .46, CostShare: .55},
	}
	data.Pricing = []ModelPricingRow{{Label: "live rates", Models: 8, Tokens: 550_000_000, Cost: pricing.Money(54_000_000_000), PricedTokens: 550_000_000}, {Label: "unpriced", Models: 2, Tokens: 100_000_000}}
	for _, row := range data.Rows {
		data.Rates = append(data.Rates, ModelRateRow{Model: row.Model, Cost: row.Cost, PricedTokens: row.PricedTokens})
		data.PerSession = append(data.PerSession, ModelPerSessionRow{Model: row.Model, Tokens: row.Tokens, Sessions: row.Sessions, TokensPerSession: row.Tokens / int64(row.Sessions)})
	}
	data.Unpriced = []ModelUnpricedRow{{Model: "model-08", Tokens: 40_000_000}, {Model: "model-09", Tokens: 35_000_000}}
	for index, row := range data.Rows {
		data.Recency = append(data.Recency, ModelRecencyRow{Model: row.Model, Days: index})
		matrix := ModelMatrixRow{Model: row.Model, Cost: row.Cost}
		for day := 0; day < 30; day++ {
			value := float64((index + 1) * (day + 1))
			switch index {
			case 0:
				if day == 10 {
					value *= 8
				}
			case 1:
				value = float64((index + 1) * (30 - day))
			case 2:
				if day >= 10 && day <= 14 {
					value = 0
				}
			}
			matrix.Values = append(matrix.Values, value)
		}
		data.Matrix.Rows = append(data.Matrix.Rows, matrix)
	}
	for day := 0; day < 30; day++ {
		data.Matrix.Dates = append(data.Matrix.Dates, time.Date(2026, time.July, 3+day, 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
	}
	return data
}
