package coding

import (
	"testing"

	"tokenhub-server/internal/model"
)

func TestCreateProviderForChannelPrefersSupplierCode(t *testing.T) {
	svc := NewCodingService(nil)

	cases := []struct {
		name         string
		supplierCode string
		endpoint     string
		want         string
	}{
		{
			name:         "baidu qianfan should not fall back to qwen",
			supplierCode: "baidu_qianfan",
			endpoint:     "https://qianfan.baidubce.com/v2",
			want:         "qianfan",
		},
		{
			name:         "tencent hunyuan uses hunyuan provider",
			supplierCode: "tencent_hunyuan",
			endpoint:     "https://api.hunyuan.cloud.tencent.com/v1",
			want:         "hunyuan",
		},
		{
			name:         "talkingdata volcengine uses doubao provider",
			supplierCode: "talkingdata",
			endpoint:     "https://modelpool-api.talkingdata.com/model/openai/api/v3",
			want:         "doubao",
		},
		{
			name:         "aliyun dashscope uses qwen provider",
			supplierCode: "aliyun_dashscope",
			endpoint:     "https://dashscope.aliyuncs.com/compatible-mode/v1",
			want:         "qwen",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := svc.CreateProviderForChannel(&model.Channel{
				Endpoint: tc.endpoint,
				APIKey:   "test-key",
				Supplier: model.Supplier{Code: tc.supplierCode},
			})
			if got := p.Name(); got != tc.want {
				t.Fatalf("provider=%s, want %s", got, tc.want)
			}
		})
	}
}
