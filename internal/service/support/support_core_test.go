package support

import (
	"context"
	"strings"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"tokenhub-server/internal/model"
	"tokenhub-server/internal/pkg/logger"
)

func TestSupportSplitMarkdownPreservesHeadingsAndIndexes(t *testing.T) {
	md := strings.Join([]string{
		"intro paragraph",
		"",
		"## Billing",
		"first billing paragraph",
		"",
		"second billing paragraph",
		"",
		"### FAQ",
		"short faq answer",
	}, "\n")

	chunks := SplitMarkdown("Guide", md, ChunkOptions{SoftMax: 35, HardMax: 80, MinChunk: 1})
	if len(chunks) < 3 {
		t.Fatalf("expected markdown to be split into at least 3 chunks, got %d: %#v", len(chunks), chunks)
	}
	if chunks[0].Title != "Guide" {
		t.Fatalf("expected intro chunk title Guide, got %q", chunks[0].Title)
	}
	if chunks[1].Title != "Guide > Billing" {
		t.Fatalf("expected billing chunk title, got %q", chunks[1].Title)
	}
	if chunks[len(chunks)-1].Title != "Guide > Billing > FAQ" {
		t.Fatalf("expected FAQ chunk title, got %q", chunks[len(chunks)-1].Title)
	}
	for i, chunk := range chunks {
		if chunk.ChunkIndex != i {
			t.Fatalf("chunk %d has index %d", i, chunk.ChunkIndex)
		}
		if strings.TrimSpace(chunk.Content) == "" {
			t.Fatalf("chunk %d has empty content", i)
		}
	}
}

func TestSupportEstimateTokensHandlesEmptyEnglishAndCJK(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty string tokens = %d, want 0", got)
	}
	if got := EstimateTokens("hello world from support tests"); got <= 0 {
		t.Fatalf("english token estimate should be positive, got %d", got)
	}
	zh := EstimateTokens("\u4f59\u989d\u5145\u503c\u5931\u8d25")
	en := EstimateTokens("balance recharge failed")
	if zh <= 0 || en <= 0 {
		t.Fatalf("token estimates should be positive, zh=%d en=%d", zh, en)
	}
}

func TestSupportBudgetGuardTransitions(t *testing.T) {
	if logger.L == nil {
		logger.L = zap.NewNop()
	}
	ctx := context.Background()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()

	guard := NewBudgetGuard(client, 1000)
	if got := guard.Check(ctx); got != BudgetNormal {
		t.Fatalf("initial budget = %s, want normal", got)
	}

	if err := guard.Deduct(ctx, 750); err != nil {
		t.Fatalf("deduct 750: %v", err)
	}
	if got := guard.Check(ctx); got != BudgetEconomy {
		t.Fatalf("budget after 75%% usage = %s, want economy", got)
	}

	if err := guard.Deduct(ctx, 210); err != nil {
		t.Fatalf("deduct 210: %v", err)
	}
	used, remaining, total := guard.UsedAndRemaining(ctx)
	if used != 960 || remaining != 40 || total != 1000 {
		t.Fatalf("used/remaining/total = %d/%d/%d, want 960/40/1000", used, remaining, total)
	}
	if got := guard.Check(ctx); got != BudgetEmergency {
		t.Fatalf("budget after 96%% usage = %s, want emergency", got)
	}
}

func TestSupportModelSelectorFiltersBudgetAndSortsPriority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SupportModelProfile{}); err != nil {
		t.Fatal(err)
	}
	rows := []model.SupportModelProfile{
		{ModelKey: "normal-high", DisplayName: "Normal", BudgetLevel: "normal", Priority: 100, IsActive: true},
		{ModelKey: "economy-mid", DisplayName: "Economy", BudgetLevel: "economy", Priority: 80, IsActive: true},
		{ModelKey: "emergency-low", DisplayName: "Emergency", BudgetLevel: "emergency", Priority: 60, IsActive: true},
		{ModelKey: "inactive", DisplayName: "Inactive", BudgetLevel: "normal", Priority: 999, IsActive: false},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.SupportModelProfile{}).
		Where("model_key = ?", "inactive").
		Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}

	selector := NewModelSelector(db)
	normal := selector.Candidates(context.Background(), BudgetNormal)
	if len(normal) != 3 || normal[0].ModelKey != "normal-high" {
		t.Fatalf("normal candidates = %#v", normal)
	}

	economy := selector.Candidates(context.Background(), BudgetEconomy)
	if len(economy) != 2 || economy[0].ModelKey != "economy-mid" || economy[1].ModelKey != "emergency-low" {
		t.Fatalf("economy candidates = %#v", economy)
	}

	emergency := selector.Primary(context.Background(), BudgetEmergency)
	if emergency == nil || emergency.ModelKey != "emergency-low" {
		t.Fatalf("emergency primary = %#v", emergency)
	}
}

func TestSupportPromptBuilderEmergencyReplyIncludesNeedHuman(t *testing.T) {
	builder := NewPromptBuilder(nil)
	if got := builder.BuildEmergencyReply("zh"); !strings.Contains(got, "<need_human/>") {
		t.Fatalf("zh emergency reply should include need_human marker: %q", got)
	}
	if got := builder.BuildEmergencyReply("en"); !strings.Contains(got, "Sorry") || !strings.Contains(got, "<need_human/>") {
		t.Fatalf("en emergency reply should include apology and need_human marker: %q", got)
	}
}
