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

// Marshal 编码 ICMP 消息，简化处理未计算校验和
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
	// 校验和置零
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
