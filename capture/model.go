// Package capture owns bounded request capture and reliable archive delivery.
// It has no dependency on CPA registration, management UI or archive SQL.
package capture

import (
	"cpa-session-archive/delivery"
	"cpa-session-archive/record"
)

type Record = record.Record
type CaptureStatus = delivery.CaptureStatus

func QueueWeight(r Record) (int64, error) { return record.QueueWeight(r) }

const (
	CaptureStatusHeader = delivery.CaptureStatusHeader
	CommitAckHeader     = delivery.CommitAckHeader
	CommitAckRequest    = delivery.CommitAckRequest
	CommitAckResponse   = delivery.CommitAckResponse
	CommitDigestHeader  = delivery.CommitDigestHeader
)
