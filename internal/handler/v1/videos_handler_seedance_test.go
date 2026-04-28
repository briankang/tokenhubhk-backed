package v1

import (
	"reflect"
	"testing"

	"tokenhub-server/internal/model"
)

// TestBuildVideoDimensions 验证 handler 把请求参数翻译为 PriceTier.DimValues 匹配键
//
// 历史背景（2026-04-28）：
//
//	旧 applySeedanceBillingTokenScale 通过缩放 token 数等价表达价差（fragile + 不可观测）
//	新方案：handler 显式打包维度 → UsageInput.Dimensions → selectPriceForTokens 按 DimValues 选档
//	这个测试锁定 buildVideoDimensions 的翻译规则，保证扣费链路不再依赖模型名 fuzzy 匹配
func TestBuildVideoDimensions(t *testing.T) {
	silent := false
	draft := true

	cases := []struct {
		name string
		req  videoGenerationRequest
		want map[string]string
	}{
		{
			name: "Seedance 1.5 silent online",
			req:  videoGenerationRequest{Model: "doubao-seedance-1.5-pro", GenerateAudio: &silent},
			want: map[string]string{
				model.DimKeyInputHasVideo: "false",
				model.DimKeyInferenceMode: "online",
				model.DimKeyAudioMode:     "false",
				model.DimKeyDraftMode:     "false",
			},
		},
		{
			name: "Seedance 1.5 silent flex (offline)",
			req:  videoGenerationRequest{Model: "doubao-seedance-1.5-pro", GenerateAudio: &silent, ServiceTier: "flex"},
			want: map[string]string{
				model.DimKeyInputHasVideo: "false",
				model.DimKeyInferenceMode: "offline",
				model.DimKeyAudioMode:     "false",
				model.DimKeyDraftMode:     "false",
			},
		},
		{
			name: "Seedance 2.0 720p with input video",
			req: videoGenerationRequest{
				Model: "doubao-seedance-2.0-720p", Resolution: "720P", VideoURL: "https://x/v.mp4",
			},
			want: map[string]string{
				model.DimKeyResolution:    "720p",
				model.DimKeyInputHasVideo: "true",
				model.DimKeyInferenceMode: "online",
				model.DimKeyAudioMode:     "true",
				model.DimKeyDraftMode:     "false",
			},
		},
		{
			name: "Seedance 2.0 1080p no video",
			req:  videoGenerationRequest{Model: "doubao-seedance-2.0-1080p", Resolution: "1080P"},
			want: map[string]string{
				model.DimKeyResolution:    "1080p",
				model.DimKeyInputHasVideo: "false",
				model.DimKeyInferenceMode: "online",
				model.DimKeyAudioMode:     "true",
				model.DimKeyDraftMode:     "false",
			},
		},
		{
			name: "Seedance 1.5 Pro Draft 480p silent",
			req: videoGenerationRequest{
				Model: "doubao-seedance-1.5-pro", Resolution: "480P",
				GenerateAudio: &silent, Draft: &draft,
			},
			want: map[string]string{
				model.DimKeyResolution:    "480p",
				model.DimKeyInputHasVideo: "false",
				model.DimKeyInferenceMode: "online",
				model.DimKeyAudioMode:     "false",
				model.DimKeyDraftMode:     "true",
			},
		},
		{
			name: "Resolution normalize: 1080P → 1080p",
			req:  videoGenerationRequest{Model: "x", Resolution: "1080P"},
			want: map[string]string{
				model.DimKeyResolution:    "1080p",
				model.DimKeyInputHasVideo: "false",
				model.DimKeyInferenceMode: "online",
				model.DimKeyAudioMode:     "true",
				model.DimKeyDraftMode:     "false",
			},
		},
		{
			name: "Resolution normalize: 720 → 720p",
			req:  videoGenerationRequest{Model: "x", Resolution: "720"},
			want: map[string]string{
				model.DimKeyResolution:    "720p",
				model.DimKeyInputHasVideo: "false",
				model.DimKeyInferenceMode: "online",
				model.DimKeyAudioMode:     "true",
				model.DimKeyDraftMode:     "false",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildVideoDimensions(tc.req)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v\nwant %v", got, tc.want)
			}
		})
	}
}

// TestBuildVideoDimensions_EmptyResolution 验证空 resolution 不被写入
func TestBuildVideoDimensions_EmptyResolution(t *testing.T) {
	got := buildVideoDimensions(videoGenerationRequest{Model: "x"})
	if _, ok := got[model.DimKeyResolution]; ok {
		t.Errorf("expected no resolution key when empty, got %v", got)
	}
}

// TestBuildVideoDimensions_OutputUsesStandardKeys 验证输出键与 model 包常量对齐
func TestBuildVideoDimensions_OutputUsesStandardKeys(t *testing.T) {
	got := buildVideoDimensions(videoGenerationRequest{Model: "x", Resolution: "720P"})
	expectedKeys := []string{
		model.DimKeyResolution, model.DimKeyInputHasVideo,
		model.DimKeyInferenceMode, model.DimKeyAudioMode, model.DimKeyDraftMode,
	}
	for _, k := range expectedKeys {
		if _, ok := got[k]; !ok {
			t.Errorf("missing standard dim key %s in output: %v", k, got)
		}
	}
}
