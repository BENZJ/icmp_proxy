package icmp

import (
	"encoding/binary"
	"errors"
	"net"
)

import "golang.org/x/net/ipv4"

// MessageBody 定义 ICMP 消息体需要实现的接口
type MessageBody interface {
	Len(proto int) int
	Marshal(proto int) ([]byte, error)
}

// Message 表示一个 ICMP 消息
type Message struct {
	Type ipv4.ICMPType
	Code int
	Body MessageBody
}

// Echo 表示 ICMP Echo 消息体
type Echo struct {
	ID   int
	Seq  int
	Data []byte
}

func (e *Echo) Len(proto int) int { return 4 + len(e.Data) }

func (e *Echo) Marshal(proto int) ([]byte, error) {
	b := make([]byte, 4+len(e.Data))
	binary.BigEndian.PutUint16(b[0:2], uint16(e.ID))
	binary.BigEndian.PutUint16(b[2:4], uint16(e.Seq))
	copy(b[4:], e.Data)
	return b, nil
}

// ListenPacket 封装 net.ListenPacket
func ListenPacket(network, address string) (net.PacketConn, error) {
	return net.ListenPacket(network, address)
}

// checksum calculates the ICMP checksum for the given data using the standard
// one's complement sum.
func checksum(b []byte) uint16 {
	var sum uint32
	for len(b) > 1 {
		sum += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) > 0 {
		sum += uint32(b[0]) << 8
	}
	for (sum >> 16) > 0 {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

// Marshal 编码 ICMP 消息并计算校验和
func (m *Message) Marshal(_ []byte) ([]byte, error) {
	if m.Body == nil {
		return nil, errors.New("nil body")
	}
	body, err := m.Body.Marshal(0)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 4+len(body))
	b[0] = byte(m.Type)
	b[1] = byte(m.Code)
	copy(b[4:], body)
	binary.BigEndian.PutUint16(b[2:4], 0)
	csum := checksum(b)
	binary.BigEndian.PutUint16(b[2:4], csum)
	return b, nil
}

// ParseMessage 解析原始 ICMP 数据
func ParseMessage(_ int, b []byte) (*Message, error) {
	if len(b) < 8 {
		return nil, errors.New("message too short")
	}
	typ := ipv4.ICMPType(b[0])
	body := &Echo{
		ID:   int(binary.BigEndian.Uint16(b[4:6])),
		Seq:  int(binary.BigEndian.Uint16(b[6:8])),
		Data: append([]byte(nil), b[8:]...),
	}
	return &Message{Type: typ, Code: int(b[1]), Body: body}, nil
}
