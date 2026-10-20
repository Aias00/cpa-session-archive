package archive

import "testing"

func TestQueueWeightAccountsBackingCapacityAndNestedMetadata(t *testing.T) {
	buffer := make([]byte, 1<<20)
	small, err := QueueWeight(Record{OriginalRequest: buffer[:1]})
	if err != nil || small < 1<<20 {
		t.Fatalf("retained backing array not charged: %d %v", small, err)
	}
	large, err := QueueWeight(Record{OriginalRequest: buffer[:1], Metadata: map[string]any{"nested": []any{string(make([]byte, 4096))}}})
	if err != nil || large-small < 4096 {
		t.Fatal("nested metadata not charged")
	}
	cycle := map[string]any{}
	cycle["cycle"] = cycle
	if _, err := QueueWeight(Record{Metadata: cycle}); err == nil {
		t.Fatal("unbounded metadata recursion accepted")
	}
}
