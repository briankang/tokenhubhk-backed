package model

import "testing"

func TestPrivacyRequestTableName(t *testing.T) {
	var req PrivacyRequest
	if req.TableName() != "privacy_requests" {
		t.Fatalf("PrivacyRequest table = %q", req.TableName())
	}
}

func TestProviderComplianceProfileTableName(t *testing.T) {
	var profile ProviderComplianceProfile
	if profile.TableName() != "provider_compliance_profiles" {
		t.Fatalf("ProviderComplianceProfile table = %q", profile.TableName())
	}
}

func TestPrivacyRequestStatusConstants(t *testing.T) {
	statuses := []string{
		PrivacyRequestStatusReceived,
		PrivacyRequestStatusVerify,
		PrivacyRequestStatusInReview,
		PrivacyRequestStatusProcessing,
		PrivacyRequestStatusCompleted,
		PrivacyRequestStatusRejected,
		PrivacyRequestStatusCancelled,
	}
	seen := map[string]bool{}
	for _, status := range statuses {
		if status == "" {
			t.Fatal("privacy request status must not be empty")
		}
		if seen[status] {
			t.Fatalf("duplicate privacy request status %q", status)
		}
		seen[status] = true
	}
}

func TestDataCollectionConstants(t *testing.T) {
	policies := []string{
		DataCollectionNone,
		DataCollectionAbuseMonitoring,
		DataCollectionTrainingPossible,
		DataCollectionUnknown,
	}
	seen := map[string]bool{}
	for _, policy := range policies {
		if policy == "" {
			t.Fatal("data collection policy must not be empty")
		}
		if seen[policy] {
			t.Fatalf("duplicate data collection policy %q", policy)
		}
		seen[policy] = true
	}
}
