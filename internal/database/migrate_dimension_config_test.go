package database

import (
	"encoding/json"
	"testing"

	"tokenhub-server/internal/model"
)

// TestInferConfigFromTiers_Seedance20 验证 Seedance 2.0 4 档的反推
func TestInferConfigFromTiers_Seedance20(t *testing.T) {
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "720p · 不含视频", DimValues: map[string]string{"resolution": "720p", "input_has_video": "false"}},
			{Name: "720p · 含视频", DimValues: map[string]string{"resolution": "720p", "input_has_video": "true"}},
			{Name: "1080p · 不含视频", DimValues: map[string]string{"resolution": "1080p", "input_has_video": "false"}},
			{Name: "1080p · 含视频", DimValues: map[string]string{"resolution": "1080p", "input_has_video": "true"}},
		},
	}
	raw, _ := json.Marshal(data)

	c := inferConfigFromTiers(raw)
	if c == nil {
		t.Fatal("expected non-nil config")
	}
	if len(c.Dimensions) != 2 {
		t.Fatalf("expected 2 dimensions, got %d", len(c.Dimensions))
	}

	// 按 key 查找（Go map 迭代不保序，不能假设具体顺序）
	resDim := c.GetDimension("resolution")
	if resDim == nil {
		t.Fatal("expected resolution dimension")
	}
	if resDim.Type != model.DimensionTypeSelect || len(resDim.Values) != 2 {
		t.Errorf("resolution: %+v, expected select with 2 values", resDim)
	}

	hasVideoDim := c.GetDimension("input_has_video")
	if hasVideoDim == nil {
		t.Fatal("expected input_has_video dimension")
	}
	if hasVideoDim.Type != model.DimensionTypeBoolean {
		t.Errorf("input_has_video: %+v, expected boolean", hasVideoDim)
	}
}

// TestInferConfigFromTiers_Seedance15Pro 4 档（inference_mode × audio_mode）
func TestInferConfigFromTiers_Seedance15Pro(t *testing.T) {
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{DimValues: map[string]string{"inference_mode": "online", "audio_mode": "true"}},
			{DimValues: map[string]string{"inference_mode": "online", "audio_mode": "false"}},
			{DimValues: map[string]string{"inference_mode": "offline", "audio_mode": "true"}},
			{DimValues: map[string]string{"inference_mode": "offline", "audio_mode": "false"}},
		},
	}
	raw, _ := json.Marshal(data)

	c := inferConfigFromTiers(raw)
	if c == nil || len(c.Dimensions) != 2 {
		t.Fatalf("expected 2 dimensions, got %+v", c)
	}

	// inference_mode → select with [online, offline]
	infMode := c.GetDimension("inference_mode")
	if infMode == nil || infMode.Type != model.DimensionTypeSelect || len(infMode.Values) != 2 {
		t.Errorf("inference_mode: %+v", infMode)
	}
	// audio_mode → boolean
	audioMode := c.GetDimension("audio_mode")
	if audioMode == nil || audioMode.Type != model.DimensionTypeBoolean {
		t.Errorf("audio_mode: %+v, expected boolean", audioMode)
	}
}

// TestInferConfigFromTiers_NoDimValues 没有 dim_values → 返回 nil
func TestInferConfigFromTiers_NoDimValues(t *testing.T) {
	max := int64(32000)
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{Name: "0-32k", InputMin: 0, InputMax: &max, OutputPrice: 4},
			{Name: "32k+", InputMin: 32000, OutputPrice: 16},
		},
	}
	raw, _ := json.Marshal(data)

	c := inferConfigFromTiers(raw)
	if c != nil {
		t.Errorf("无 dim_values 应返回 nil, got %+v", c)
	}
}

// TestInferConfigFromTiers_EmptyValueSkipped 空值通配不计入维度
func TestInferConfigFromTiers_EmptyValueSkipped(t *testing.T) {
	data := model.PriceTiersData{
		Tiers: []model.PriceTier{
			{DimValues: map[string]string{"resolution": "1080p", "audio_mode": ""}},
			{DimValues: map[string]string{"resolution": "720p", "audio_mode": ""}},
		},
	}
	raw, _ := json.Marshal(data)

	c := inferConfigFromTiers(raw)
	if c == nil {
		t.Fatal("expected non-nil")
	}
	// audio_mode 全空 → 不该出现在 Dimensions 中
	for _, d := range c.Dimensions {
		if d.Key == "audio_mode" {
			t.Errorf("audio_mode 全为空值不应被声明为维度, got dim: %+v", d)
		}
	}
}

// TestDefaultConfigForModelType 验证默认模板
func TestDefaultConfigForModelType(t *testing.T) {
	c := defaultConfigForModelType("VideoGeneration", "doubao-seedance-2.0")
	if c == nil || len(c.Dimensions) == 0 {
		t.Fatal("VideoGeneration 应有默认维度")
	}

	c2 := defaultConfigForModelType("LLM", "qwen3-max")
	if c2 != nil {
		t.Error("LLM 不应有默认维度（避免误导 thinking_mode 支持）")
	}

	c3 := defaultConfigForModelType("ImageGeneration", "qwen-image")
	if c3 != nil {
		t.Error("ImageGeneration 不应有默认维度")
	}
}

// TestIsBooleanValueSet 验证 boolean 识别
func TestIsBooleanValueSet(t *testing.T) {
	cases := []struct {
		values []string
		want   bool
	}{
		{[]string{"true", "false"}, true},
		{[]string{"false", "true"}, true},
		{[]string{"true"}, true},
		{[]string{"false"}, true},
		{[]string{"yes", "no"}, false},
		{[]string{"true", "false", "maybe"}, false},
		{[]string{}, false},
		{[]string{"720p", "1080p"}, false},
	}
	for _, c := range cases {
		got := isBooleanValueSet(c.values)
		if got != c.want {
			t.Errorf("values=%v: got %v, want %v", c.values, got, c.want)
		}
	}
}

// TestHasNonEmptyDimensionConfig 验证幂等检查
func TestHasNonEmptyDimensionConfig(t *testing.T) {
	if hasNonEmptyDimensionConfig(nil) {
		t.Error("nil should be empty")
	}
	if hasNonEmptyDimensionConfig([]byte("null")) {
		t.Error("'null' should be empty")
	}
	if hasNonEmptyDimensionConfig([]byte(`{"schema_version":1,"dimensions":[]}`)) {
		t.Error("empty dimensions array should be considered empty")
	}
	c := model.ModelDimensionConfig{
		SchemaVersion: 1,
		Dimensions:    []model.DimensionDefinition{{Key: "resolution"}},
	}
	raw, _ := json.Marshal(c)
	if !hasNonEmptyDimensionConfig(raw) {
		t.Error("non-empty dimensions should not be empty")
	}
}
