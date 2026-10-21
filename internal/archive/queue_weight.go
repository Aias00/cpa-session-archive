package archive

import "cpa-session-archive/record"

func QueueWeight(r Record) (int64, error) { return record.QueueWeight(r) }

var ErrQueueMetadata = record.ErrQueueMetadata
