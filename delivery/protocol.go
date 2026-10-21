package delivery

// Commit acknowledgement is opt-in. A receipt confirms archive transaction
// success, not projection catch-up or power-loss durability of SQLite NORMAL.
const (
	CommitAckHeader    = "X-CPA-Archive-Ack"
	CommitAckRequest   = "commit-v1"
	CommitAckResponse  = "committed-v1"
	CommitDigestHeader = "X-CPA-Archive-Content-SHA256"
)
