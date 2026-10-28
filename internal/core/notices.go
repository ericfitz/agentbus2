package core

// The operator notice kinds (the Store's notices table). Status and the health
// document read every kind of NoticeKinds, so a new kind is added here once.
const (
	NoticeCapacity   = "capacity"
	NoticeEmbedding  = "embedding"
	NoticeInspection = "inspection"
)

// NoticeKinds lists the notice kinds Status and the health document report, in
// their reporting order.
func NoticeKinds() []string {
	return []string{NoticeCapacity, NoticeEmbedding, NoticeInspection}
}
