package ipv4

// ICMPType represents ICMP message type.
type ICMPType uint8

const (
	ICMPTypeEchoReply ICMPType = 0
	ICMPTypeEcho      ICMPType = 8
)

const ProtocolICMP = 1

func (typ ICMPType) Protocol() int { return ProtocolICMP }
