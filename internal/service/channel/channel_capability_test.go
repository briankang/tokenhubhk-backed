package channel

import (
	"testing"

	"tokenhub-server/internal/model"
)

func TestFilterByCapabilityBeforeStrategy(t *testing.T) {
	router := &ChannelRouter{}
	chatOnly := &model.Channel{SupportedCapabilities: model.CapabilityChat}
	imageCapable := &model.Channel{SupportedCapabilities: model.CapabilityChat + "," + model.CapabilityImage}

	candidates := []RouteCandidate{
		{Channel: chatOnly, ActualModel: "text-model", Priority: 100, Weight: 100},
		{Channel: imageCapable, ActualModel: "image-model", Priority: 1, Weight: 1},
	}

	filtered := router.filterByCapability(candidates, model.CapabilityImage)
	if len(filtered) != 1 {
		t.Fatalf("filtered candidates=%d, want 1", len(filtered))
	}
	if filtered[0].Channel != imageCapable {
		t.Fatalf("capability filter should keep image-capable channel")
	}
}

func TestFilterByCapabilityEmptyKeepsCandidates(t *testing.T) {
	router := &ChannelRouter{}
	candidates := []RouteCandidate{{Channel: &model.Channel{SupportedCapabilities: model.CapabilityChat}}}

	filtered := router.filterByCapability(candidates, "")
	if len(filtered) != len(candidates) {
		t.Fatalf("empty capability should keep candidates")
	}
}
