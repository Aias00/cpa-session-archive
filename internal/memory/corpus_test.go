package memory

import (
	"context"
	"fmt"
	"testing"

	"cpa-session-archive/internal/archive"
)

// This synthetic regression corpus checks literal retrieval, not semantic recall
// or production quality. Each expected request is assigned before indexing.
func TestRecallLiteralCorpus(t *testing.T) {
	e, raw, _ := testEngine(t)
	fixtures := []struct{ query, text string }{
		{"会话记忆", "决定先实现会话记忆检索"},
		{"中文搜索", "中文搜索使用字符索引"},
		{"回填", "历史数据回填完成后核对数量"},
		{"记", "记住明确的项目范围"},
		{"权限", "权限不足时不返回内容"},
		{"异步采集", "异步采集存在队列丢失风险"},
		{"项目隔离", "项目隔离通过稳定标识实现"},
		{"长期记忆", "长期记忆需要人工确认"},
		{"模型预算", "模型预算尚未启用"},
		{"未通过", "这个检查未通过，需要继续修复"},
		{"分支", "分支没有合并"},
		{"删除来源", "删除来源立即隐藏对应摘录"},
		{"更正决定", "更正决定时保留历史证据"},
		{"状态码", "状态码表示实际请求结果"},
		{"人工确认", "只有人工确认的偏好才会激活"},
		{"索引延迟", "页面需要展示索引延迟"},
		{"证据引用", "证据引用指向保留的版本"},
		{"本地代理", "本地代理支持不同客户端"},
		{"线程恢复", "没有实现原生线程恢复"},
		{"工具结果", "工具结果包含执行失败的信息"},
		{"SQLITE_BUSY", "database returned SQLITE_BUSY"},
		{"ECONNRESET", "transport failure ECONNRESET"},
		{"ERR_CONNECTION_REFUSED", "browser: ERR_CONNECTION_REFUSED"},
		{"/Users/me/repo/main.go", "edit /Users/me/repo/main.go before build"},
		{"internal/memory/store.go", "source path internal/memory/store.go"},
		{"401 Unauthorized", "HTTP 401 Unauthorized during read"},
		{"response.completed", "use the response.completed event"},
		{"archive.sqlite-wal", "backup must include archive.sqlite-wal consistency"},
		{"X-Request-Id", "the header X-Request-Id identifies this request"},
		{"go test ./...", "verified with go test ./..."},
	}
	records := []archive.Record{}
	for i, fixture := range fixtures {
		records = append(records, sample(fmt.Sprintf("corpus-%02d", i), "corpus", "corpus-project", fixture.text))
	}
	if err := raw.PutBatch(records); err != nil {
		t.Fatal(err)
	}
	syncAll(t, e)
	correct := 0
	for i, fixture := range fixtures {
		result, err := e.Search(context.Background(), Query{Text: fixture.query, ProjectID: "corpus-project", Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range result.Hits {
			if hit.Citation.RequestID == fmt.Sprintf("corpus-%02d", i) {
				found = true
				break
			}
		}
		if found {
			correct++
		} else {
			t.Errorf("missing literal %q: %+v", fixture.query, result.Hits)
		}
	}
	t.Logf("synthetic literal corpus top-5: %d/%d", correct, len(fixtures))
}
