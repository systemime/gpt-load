package scheduler

import (
	"errors"
	"testing"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
	"gpt-load/internal/state"
)

func slotTestQuery(slots *state.CredentialSlots) Query {
	return Query{
		ClientProtocol:  protocol.OpenAICompletions,
		Operation:       execution.OperationChatCompletion,
		ExternalModel:   modelPointer("gpt-4o"),
		CredentialSlots: slots,
	}
}

func TestIteratorSkipsCredentialsAtSlotCapacity(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1}, {ID: 12, GroupID: 1},
	}}
	slots := state.NewCredentialSlots(1)
	if !slots.TryAcquire(11, 1) {
		t.Fatal("occupy key 11 failed")
	}
	iterator := New(schedulerSnapshot(), source, slotTestQuery(slots))
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 12 {
		t.Fatalf("Next() = (%#v, %v), want key 12 (11 at capacity)", selection, err)
	}
}

func TestIteratorHonorsPreferredOnlyBelowSlotCapacity(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1}, {ID: 12, GroupID: 1},
	}}
	slots := state.NewCredentialSlots(1)
	query := slotTestQuery(slots)
	query.PreferredCredentialID = 11

	fresh := New(schedulerSnapshot(), source, query)
	selection, err := fresh.Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("Next() = (%#v, %v), want preferred key 11", selection, err)
	}

	if !slots.TryAcquire(11, 1) {
		t.Fatal("occupy key 11 failed")
	}
	capped := New(schedulerSnapshot(), source, query)
	selection, err = capped.Next()
	if err != nil || selection.CredentialID != 12 {
		t.Fatalf("Next() = (%#v, %v), want fallback key 12 (preferred at capacity)", selection, err)
	}
}

func TestIteratorServesWhenAllCredentialsCapped(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1}, {ID: 12, GroupID: 1},
	}}
	slots := state.NewCredentialSlots(1)
	slots.TryAcquire(11, 1)
	slots.TryAcquire(12, 1)
	iterator := New(schedulerSnapshot(), source, slotTestQuery(slots))
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID == 0 {
		t.Fatalf("Next() with all keys capped = (%#v, %v), want a selection instead of exhaustion", selection, err)
	}
	if !iterator.SelectionBeyondCap() {
		t.Fatal("SelectionBeyondCap() = false, want exemption flag for admission layer")
	}
}

func TestIteratorBeyondCapFlagFalseWhileSlotsAvailable(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
	}}
	slots := state.NewCredentialSlots(1)
	iterator := New(schedulerSnapshot(), source, slotTestQuery(slots))
	if _, err := iterator.Next(); err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if iterator.SelectionBeyondCap() {
		t.Fatal("SelectionBeyondCap() = true with free slots")
	}
}

func TestIteratorIgnoresNilSlotSource(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
	}}
	iterator := New(schedulerSnapshot(), source, slotTestQuery(nil))
	if _, err := iterator.Next(); errors.Is(err, ErrExhausted) {
		t.Fatalf("Next() with nil slots error = %v", err)
	}
}

func TestIteratorUnlimitedSlotsAdmitEverything(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1},
	}}
	slots := state.NewCredentialSlots(0)
	for i := 0; i < 5; i++ {
		slots.TryAcquire(11, 1)
	}
	iterator := New(schedulerSnapshot(), source, slotTestQuery(slots))
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("Next() under unlimited cap = (%#v, %v), want key 11", selection, err)
	}
}

type staticUsageView map[uint]state.CredentialUsage

func (view staticUsageView) Usage(id uint) state.CredentialUsage {
	return view[id]
}

func TestIteratorUsageBalancePrefersLessConsumedCredential(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1}, {ID: 12, GroupID: 1},
	}}
	// score = (在途/cap)×P: 在途并列(各 1)时由 5h token 消耗 P 决出低分者。
	slots := state.NewCredentialSlots(4)
	slots.TryAcquire(11, 1)
	slots.TryAcquire(12, 1)
	usage := staticUsageView{11: {Tokens5h: 900}, 12: {Tokens5h: 100}}
	query := slotTestQuery(slots)
	query.CredentialUsage = usage
	query.UsageBalance = true
	iterator := New(schedulerSnapshot(), source, query)
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 12 {
		t.Fatalf("Next() = (%#v, %v), want less-consumed key 12", selection, err)
	}
}

func TestIteratorUsageBalanceIgnoresWeightsUnderScoreFormula(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1, WeightManual: modelWeightPointer(1)},
		{ID: 12, GroupID: 1, WeightManual: modelWeightPointer(4)},
	}}
	// 综合分公式不含权重维度: 同在途下 12 消耗更高即被避让, 权重只影响公平序并列。
	slots := state.NewCredentialSlots(4)
	slots.TryAcquire(11, 1)
	slots.TryAcquire(12, 1)
	usage := staticUsageView{11: {Tokens5h: 200}, 12: {Tokens5h: 300}}
	query := slotTestQuery(slots)
	query.CredentialUsage = usage
	query.UsageBalance = true
	iterator := New(schedulerSnapshot(), source, query)
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("Next() = (%#v, %v), want lower-score key 11 regardless of weight", selection, err)
	}
}

func TestIteratorUsageBalanceRespectsPreferredAffinity(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1}, {ID: 12, GroupID: 1},
	}}
	usage := staticUsageView{11: {Requests5h: 900, Tokens5h: 900}, 12: {Requests5h: 100, Tokens5h: 100}}
	query := slotTestQuery(nil)
	query.CredentialUsage = usage
	query.UsageBalance = true
	query.PreferredCredentialID = 11
	iterator := New(schedulerSnapshot(), source, query)
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("Next() = (%#v, %v), want preferred key 11 to win over usage pressure", selection, err)
	}
}

func TestIteratorUsageBalanceFallsBackWhenNoUsage(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1}, {ID: 12, GroupID: 1},
	}}
	query := slotTestQuery(nil)
	query.CredentialUsage = staticUsageView{}
	query.UsageBalance = true
	iterator := New(schedulerSnapshot(), source, query)
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("Next() = (%#v, %v), want fair-order key 11 with zero usage", selection, err)
	}
}

func TestIteratorUsageBalanceDisabledKeepsFairOrder(t *testing.T) {
	source := fakeCredentialSource{keys: []state.CredentialMeta{
		{ID: 11, GroupID: 1}, {ID: 12, GroupID: 1},
	}}
	usage := staticUsageView{11: {Requests5h: 900, Tokens5h: 900}, 12: {Requests5h: 100, Tokens5h: 100}}
	query := slotTestQuery(nil)
	query.CredentialUsage = usage
	query.UsageBalance = false
	iterator := New(schedulerSnapshot(), source, query)
	selection, err := iterator.Next()
	if err != nil || selection.CredentialID != 11 {
		t.Fatalf("Next() = (%#v, %v), want fair-order key 11 when balance disabled", selection, err)
	}
}

func modelWeightPointer(value int) *int {
	return &value
}
