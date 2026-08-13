package agent

type InboxBatch struct {
	MessageIDs []uint64
	Text       string
}

type InboxSource interface {
	SnapshotInbox() InboxBatch
	AcknowledgeInbox(InboxBatch) error
	SettleDelivery() bool
	ActiveContext() string
}
