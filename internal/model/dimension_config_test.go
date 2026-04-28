package model

import (
	"reflect"
	"testing"
)

// TestDimensionConfig_HasDimension 验证 HasDimension 查找
func TestDimensionConfig_HasDimension(t *testing.T) {
	c := &ModelDimensionConfig{
		Dimensions: []DimensionDefinition{
			{Key: DimKeyResolution, Type: DimensionTypeSelect},
			{Key: DimKeyInputHasVideo, Type: DimensionTypeBoolean},
		},
	}
	if !c.HasDimension(DimKeyResolution) {
		t.Error("expected HasDimension(resolution)=true")
	}
	if c.HasDimension("nonexistent") {
		t.Error("expected HasDimension(nonexistent)=false")
	}
	// nil-safe
	var nilC *ModelDimensionConfig
	if nilC.HasDimension(DimKeyResolution) {
		t.Error("nil receiver should return false")
	}
}

// TestDimensionConfig_GetDimension 验证按 key 查找定义
func TestDimensionConfig_GetDimension(t *testing.T) {
	c := &ModelDimensionConfig{
		Dimensions: []DimensionDefinition{
			{Key: DimKeyResolution, Label: "分辨率", Type: DimensionTypeSelect, Values: []string{"720p", "1080p"}},
		},
	}
	d := c.GetDimension(DimKeyResolution)
	if d == nil {
		t.Fatal("expected non-nil dimension")
	}
	if d.Label != "分辨率" {
		t.Errorf("label = %s, want 分辨率", d.Label)
	}
	if !reflect.DeepEqual(d.Values, []string{"720p", "1080p"}) {
		t.Errorf("values = %v", d.Values)
	}
	if c.GetDimension("nope") != nil {
		t.Error("missing key should return nil")
	}
}

// TestDimensionConfig_ApplyDefaults 验证默认值填充
func TestDimensionConfig_ApplyDefaults(t *testing.T) {
	c := &ModelDimensionConfig{
		Dimensions: []DimensionDefinition{
			{Key: DimKeyResolution, Default: stringPtr("720p")},
			{Key: DimKeyInputHasVideo, Default: stringPtr("false")},
			{Key: "no_default"}, // 没有默认值
		},
	}

	// 用户只传 resolution
	dims := map[string]string{"resolution": "1080p"}
	out := c.ApplyDefaults(dims)
	if out["resolution"] != "1080p" {
		t.Error("用户传值不应被默认值覆盖")
	}
	if out["input_has_video"] != "false" {
		t.Errorf("input_has_video 应该填默认值 false, got %v", out["input_has_video"])
	}
	if _, ok := out["no_default"]; ok {
		t.Error("无默认值的维度不应该被填充")
	}
}

// TestDimensionConfig_ApplyDefaults_NilMap 验证 nil dims 处理
func TestDimensionConfig_ApplyDefaults_NilMap(t *testing.T) {
	c := &ModelDimensionConfig{
		Dimensions: []DimensionDefinition{
			{Key: "x", Default: stringPtr("default_x")},
		},
	}
	out := c.ApplyDefaults(nil)
	if out == nil {
		t.Fatal("expected non-nil output map")
	}
	if out["x"] != "default_x" {
		t.Errorf("default not applied to nil input, got %v", out)
	}
}

// TestDimensionConfig_ApplyDefaults_NilConfig 验证 nil config 不崩溃
func TestDimensionConfig_ApplyDefaults_NilConfig(t *testing.T) {
	var c *ModelDimensionConfig
	dims := map[string]string{"a": "b"}
	out := c.ApplyDefaults(dims)
	if !reflect.DeepEqual(out, dims) {
		t.Errorf("nil config should return input unchanged, got %v", out)
	}
}

// TestVideoGenerationDefaultDimensions 验证默认视频维度模板的完整性
func TestVideoGenerationDefaultDimensions(t *testing.T) {
	dims := VideoGenerationDefaultDimensions()
	expectedKeys := []string{
		DimKeyResolution, DimKeyInputHasVideo, DimKeyInferenceMode,
		DimKeyAudioMode, DimKeyDraftMode,
	}
	if len(dims) != len(expectedKeys) {
		t.Fatalf("expected %d dimensions, got %d", len(expectedKeys), len(dims))
	}
	for i, key := range expectedKeys {
		if dims[i].Key != key {
			t.Errorf("dim[%d].Key = %s, want %s", i, dims[i].Key, key)
		}
	}
	// 每个维度应有 Label / Default
	for _, d := range dims {
		if d.Label == "" {
			t.Errorf("dimension %s missing Label", d.Key)
		}
		if d.Default == nil {
			t.Errorf("dimension %s missing Default", d.Key)
		}
	}
}

// TestLLMDefaultDimensions 验证 LLM 默认维度模板包含 thinking_mode（防回归）
func TestLLMDefaultDimensions(t *testing.T) {
	dims := LLMDefaultDimensions()
	hasThinking := false
	for _, d := range dims {
		if d.Key == DimKeyThinkingMode {
			hasThinking = true
			if d.Type != DimensionTypeBoolean {
				t.Errorf("thinking_mode should be boolean, got %s", d.Type)
			}
		}
	}
	if !hasThinking {
		t.Error("LLM 默认维度必须包含 thinking_mode")
	}
}
