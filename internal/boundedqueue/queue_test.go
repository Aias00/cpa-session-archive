package boundedqueue

import "testing"

func TestReservationsIncludeInFlightAndReleaseOnlyOnce(t *testing.T) {
	q, err := New[[]byte](2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if q.TryPush([]byte{1}, 7) != Accepted {
		t.Fatal("initial admission")
	}
	item := <-q.Items()
	if q.TryPush([]byte{2}, 4) != ByteLimit || q.Snapshot().RetainedBytes != 7 {
		t.Fatal("dequeue released in-flight reservation")
	}
	if err := q.SetMaxBytes(3); err != nil {
		t.Fatal(err)
	}
	if q.TryPush([]byte{2}, 1) != ByteLimit {
		t.Fatal("lowered limit discarded pending weight")
	}
	item.Done()
	item.Done()
	if item.Value != nil || q.Snapshot().RetainedBytes != 0 {
		t.Fatal("release retained payload or double-released")
	}
	if q.TryPush([]byte{3}, 4) != Oversize {
		t.Fatal("oversize item admitted")
	}
	if q.TryPush([]byte{3}, 3) != Accepted {
		t.Fatal("new budget not reusable")
	}
	(<-q.Items()).Done()
}

func TestCountRefusalDoesNotLeakWeight(t *testing.T) {
	q, err := New[string](1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if q.TryPush("one", 1) != Accepted || q.TryPush("two", 2) != CountLimit || q.Snapshot().RetainedBytes != 1 {
		t.Fatal("count refusal leaked bytes")
	}
	(<-q.Items()).Done()
	if q.Snapshot().RetainedBytes != 0 {
		t.Fatal("reservation not released")
	}
	if _, err := New[string](0, 100); err == nil {
		t.Fatal("invalid queue accepted")
	}
}
