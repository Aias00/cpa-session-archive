package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"cpa-session-archive/internal/archive"
	"cpa-session-archive/internal/storage"
)

func BenchmarkRecallTenThousandChunks(b *testing.B) {
	dir := b.TempDir()
	raw, err := archive.OpenStore(filepath.Join(dir, "archive.sqlite"), false)
	if err != nil {
		b.Fatal(err)
	}
	defer raw.DB.Close()
	engine, err := Open(filepath.Join(dir, "memory.sqlite"), storage.NewSQLite(raw))
	if err != nil {
		b.Fatal(err)
	}
	defer engine.Close()
	records := make([]archive.Record, 1000)
	for i := range records {
		messages := []map[string]string{}
		for j := 0; j < 10; j++ {
			messages = append(messages, map[string]string{"role": "user", "content": fmt.Sprintf("记录消息 专用标记%d-%d needle%d item%d", i, j, i, j)})
		}
		payload, _ := json.Marshal(map[string]any{"messages": messages})
		records[i] = sample(fmt.Sprintf("bench-%d", i), "benchmark", "benchmark-project", "")
		records[i].OriginalRequest = payload
	}
	if err := raw.PutBatch(records); err != nil {
		b.Fatal(err)
	}
	for {
		n, err := engine.Sync(context.Background(), 128)
		if err != nil {
			b.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	status, err := engine.Status(context.Background())
	if err != nil || status.Chunks != 10000 {
		b.Fatalf("%+v %v", status, err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := engine.Search(context.Background(), Query{Text: fmt.Sprintf("needle%d", i%1000), ProjectID: "benchmark-project", Limit: 5})
		if err != nil || len(result.Hits) != 5 {
			b.Fatalf("%+v %v", result, err)
		}
	}
}
