package admin

import (
	"net/http"
	"testing"

	"github.com/codex2api/internal/grokquota"
)

func TestShouldMarkUsageProbeAccountError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		want       bool
	}{
		{
			name:       "payment required deactivated workspace",
			statusCode: http.StatusPaymentRequired,
			body:       []byte(`{"detail":{"code":"deactivated_workspace"}}`),
			want:       true,
		},
		{
			name:       "forbidden deactivated workspace",
			statusCode: http.StatusForbidden,
			body:       []byte(`{"error":{"code":"deactivated_workspace"}}`),
			want:       true,
		},
		{
			name:       "generic payment required is not account error",
			statusCode: http.StatusPaymentRequired,
			body:       []byte(`{"error":{"code":"billing_hard_limit_reached"}}`),
			want:       false,
		},
		{
			name:       "rate limit handled separately",
			statusCode: http.StatusTooManyRequests,
			body:       []byte(`{"detail":{"code":"deactivated_workspace"}}`),
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldMarkUsageProbeAccountError(tt.statusCode, tt.body); got != tt.want {
				t.Fatalf("shouldMarkUsageProbeAccountError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyGrokUsageSnapshotBillingProjectsWeeklyUsage(t *testing.T) {
	resetAt := "2026-07-08T03:40:00Z"
	pct := 63.0
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source:         grokquota.SourceGrokBuildBilling,
		State:          grokquota.StateObserved,
		UsedPercent:    &pct,
		APIUsedPercent: &pct,
		Window:         &grokquota.TimeWindow{ResetAt: resetAt},
	})

	if resp.UsagePercent7d == nil || *resp.UsagePercent7d != 63 {
		t.Fatalf("UsagePercent7d = %v, want 63", resp.UsagePercent7d)
	}
	if resp.Reset7dAt != resetAt {
		t.Fatalf("Reset7dAt = %q, want %q", resp.Reset7dAt, resetAt)
	}
}

func TestApplyGrokUsageSnapshotBillingZeroPercentProjectsReset(t *testing.T) {
	resetAt := "2026-07-09T03:33:00Z"
	pct := 0.0
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source:      grokquota.SourceGrokBuildBilling,
		State:       grokquota.StateObserved,
		UsedPercent: &pct,
		Window:      &grokquota.TimeWindow{ResetAt: resetAt},
	})

	if resp.UsagePercent7d == nil || *resp.UsagePercent7d != 0 {
		t.Fatalf("UsagePercent7d = %v, want 0", resp.UsagePercent7d)
	}
	if resp.Reset7dAt != resetAt {
		t.Fatalf("Reset7dAt = %q, want %q", resp.Reset7dAt, resetAt)
	}
}

func TestApplyGrokUsageSnapshotHeaderObserverDoesNotProjectWeeklyUsage(t *testing.T) {
	pct := 60.0
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source:         grokquota.SourceHeaderObserver,
		State:          grokquota.StateObserved,
		APIUsedPercent: &pct,
		Headers: map[string]string{
			"x-ratelimit-limit-requests":     "100",
			"x-ratelimit-remaining-requests": "40",
		},
	})

	if resp.UsagePercent7d != nil {
		t.Fatalf("UsagePercent7d = %v, want nil for header observer", *resp.UsagePercent7d)
	}
	if resp.Reset7dAt != "" {
		t.Fatalf("Reset7dAt = %q, want empty", resp.Reset7dAt)
	}
}

func TestApplyGrokUsageSnapshotBillingWithoutPercentDoesNotProjectWeeklyUsage(t *testing.T) {
	resp := &accountResponse{}

	applyGrokUsageSnapshotToQuotaFields(resp, &grokquota.UsageSnapshot{
		Source: grokquota.SourceGrokBuildBilling,
		State:  grokquota.StateObserved,
		Window: &grokquota.TimeWindow{ResetAt: "2026-07-08T03:40:00Z"},
	})

	if resp.UsagePercent7d != nil {
		t.Fatalf("UsagePercent7d = %v, want nil without billing percent", *resp.UsagePercent7d)
	}
	if resp.Reset7dAt != "" || resp.Window7dKind != "" || resp.Window7dSeconds != nil {
		t.Fatalf("unexpected projected window: reset=%q kind=%q seconds=%v", resp.Reset7dAt, resp.Window7dKind, resp.Window7dSeconds)
	}
}
