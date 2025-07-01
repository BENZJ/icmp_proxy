package main

import (
	"icmptun/pkg/protocol"
	"net"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// mockPacketConn is a mock implementation of our icmpConn interface for testing.
type mockPacketConn struct {
	mu        sync.Mutex
	written   []byte
	writeAddr net.Addr
}

// WriteTo satisfies the icmpConn interface.
func (m *mockPacketConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.written = make([]byte, len(p))
	copy(m.written, p)
	m.writeAddr = addr
	return len(p), nil
}

// TestHandlePacket tests the core logic of forwarding data and sending a reply.
func TestHandlePacket(t *testing.T) {
	// 1. Set up a mock TCP server to act as the target service
	mockTarget, err := net.Listen("tcp", protocol.TargetServiceAddr)
	if err != nil {
		t.Fatalf("Failed to set up mock TCP server: %v", err)
	}
	defer mockTarget.Close()

	go func() {
		conn, err := mockTarget.Accept()
		if err != nil {
			return // Listener closed, just exit
		}
		defer conn.Close()
		// Echo the received data back as the response
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		conn.Write(buf[:n])
	}()

	// 2. Set up test data
	clientAddr := &net.IPAddr{IP: net.ParseIP("127.0.0.1")}
	requestData := []byte("hello world")
	requestEcho := &icmp.Echo{
		ID:   protocol.MagicID,
		Seq:  123,
		Data: requestData,
	}

	// 3. Create a mock ICMP connection
	mockConn := &mockPacketConn{}

	// 4. Call the function to be tested
	handlePacket(mockConn, clientAddr, requestEcho)

	// 5. Give the handler a moment to process
	time.Sleep(100 * time.Millisecond)

	// 6. Verify the results
	mockConn.mu.Lock()
	defer mockConn.mu.Unlock()

	if mockConn.written == nil {
		t.Fatal("handlePacket did not write any data back")
	}

	if mockConn.writeAddr.String() != clientAddr.String() {
		t.Errorf("Expected write address to be %s, but got %s", clientAddr.String(), mockConn.writeAddr.String())
	}

	// Parse the written data to check if it's a valid ICMP reply
	// Note: The protocol number for ICMP Echo Reply is the same as for Echo Request.
	replyMsg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), mockConn.written)
	if err != nil {
		t.Fatalf("Failed to parse written ICMP message: %v", err)
	}

	if replyMsg.Type != ipv4.ICMPTypeEchoReply {
		t.Errorf("Expected ICMP message type to be EchoReply, but got %v", replyMsg.Type)
	}

	replyEcho, ok := replyMsg.Body.(*icmp.Echo)
	if !ok {
		t.Fatal("Written ICMP message body is not of type *icmp.Echo")
	}

	if replyEcho.ID != protocol.MagicID {
		t.Errorf("Expected reply ID to be %d, but got %d", protocol.MagicID, replyEcho.ID)
	}

	if replyEcho.Seq != requestEcho.Seq {
		t.Errorf("Expected reply sequence to be %d, but got %d", requestEcho.Seq, replyEcho.Seq)
	}

	if string(replyEcho.Data) != string(requestData) {
		t.Errorf("Expected reply data to be '%s', but got '%s'", string(requestData), string(replyEcho.Data))
	}

	t.Log("TestHandlePacket successful!")
}

