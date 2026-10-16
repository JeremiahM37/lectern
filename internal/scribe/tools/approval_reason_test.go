package tools

import (
	"context"
	"strings"
	"testing"
)

func TestReasonSlotCarriesTheHostsReason(t *testing.T) {
	ctx, reason := WithReasonSlot(context.Background())
	SetApprovalReason(ctx, "use a dry run")
	if got := reason(); got != "use a dry run" {
		t.Fatalf("reason = %q", got)
	}
	if got := DeniedWithReason(reason()); got != " Reason: use a dry run" {
		t.Fatalf("tail = %q", got)
	}
}

func TestSetWithoutSlotIsANoOp(t *testing.T) {
	SetApprovalReason(context.Background(), "nobody is listening")
	if DeniedWithReason("") != "" {
		t.Fatal("an empty reason adds text")
	}
}

func TestLongReasonIsCut(t *testing.T) {
	ctx, reason := WithReasonSlot(context.Background())
	SetApprovalReason(ctx, strings.Repeat("x", 800))
	if n := len(reason()); n > 510 {
		t.Fatalf("reason is %d bytes, want it cut near 500", n)
	}
}
