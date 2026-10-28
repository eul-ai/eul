package agent

type InboxBatch struct {
	MessageIDs   []uint64
	Text         string
	Instructions string
}

type Inbox interface {
	Snapshot() InboxBatch
	Acknowledge(InboxBatch) error
}
