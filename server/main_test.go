package main

import (
	"bufio"
	"bytes"
	"icmptun/pkg/protocol"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"sort"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// mockIcmpConn captures all packets written to it, allowing for inspection.
type mockIcmpConn struct {
	mu      sync.Mutex
	packets [][]byte
	addr    net.Addr
}

// WriteTo satisfies the icmpConn interface and stores the written packet.
func (m *mockIcmpConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Store a copy of the packet
	packetCopy := make([]byte, len(p))
	copy(packetCopy, p)
	m.packets = append(m.packets, packetCopy)
	m.addr = addr
	return len(p), nil
}

// GetPackets returns a copy of the captured packets for safe inspection.
func (m *mockIcmpConn) GetPackets() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.packets...)
}

// TestHandleHttpRequest_Chunking tests the full proxy logic including response chunking.
func TestHandleHttpRequest_Chunking(t *testing.T) {
	// 1. Set up a mock HTTP server that returns a large response.
	responseBody := bytes.Repeat([]byte("0123456789"), 300) // 3000 bytes response
	mockHTTPServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	}))
	defer mockHTTPServer.Close()

	// 2. Create a valid HTTP GET request and dump it to bytes.
	req, err := http.NewRequest("GET", mockHTTPServer.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create HTTP request: %v", err)
	}
	reqBytes, err := httputil.DumpRequest(req, true)
	if err != nil {
		t.Fatalf("Failed to dump HTTP request: %v", err)
	}

	// 3. Prepare the incoming ICMP packet.
	clientAddr := &net.IPAddr{IP: net.ParseIP("127.0.0.1")}
	requestPacket := &icmp.Echo{
		ID:   protocol.MagicID,
		Seq:  1, // The sequence of the request itself doesn't matter for the response
		Data: reqBytes,
	}

	// 4. Create the mock ICMP connection and call the handler.
	mockConn := &mockIcmpConn{}
	handleHttpRequest(mockConn, clientAddr, requestPacket)

	// 5. Give the handler time to process and send all chunks.
	time.Sleep(200 * time.Millisecond)

	// 6. Verify the results.
	packets := mockConn.GetPackets()
	if len(packets) < 3 { // Should be at least 2 data chunks + 1 final chunk
		t.Fatalf("Expected at least 3 packets for a chunked response, but got %d", len(packets))
	}

	// Reassemble the response from the chunks
	var reassembledBody []byte
	var receivedChunks []*icmp.Echo

	for i, packetBytes := range packets {
		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), packetBytes)
		if err != nil {
			t.Fatalf("Packet #%d: Failed to parse ICMP message: %v", i, err)
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok {
			t.Fatalf("Packet #%d: Message body is not *icmp.Echo", i)
		}
		if echo.ID != protocol.MagicID {
			t.Errorf("Packet #%d: Expected ID %d, got %d", i, protocol.MagicID, echo.ID)
		}
		receivedChunks = append(receivedChunks, echo)
	}

	// Sort chunks by sequence number to handle out-of-order delivery if it ever occurs.
	sort.Slice(receivedChunks, func(i, j int) bool {
		return receivedChunks[i].Seq < receivedChunks[j].Seq
	})

	// Check for the final zero-length packet
	lastChunk := receivedChunks[len(receivedChunks)-1]
	if len(lastChunk.Data) != 0 {
		t.Errorf("Expected the last packet to be zero-length, but it had length %d", len(lastChunk.Data))
	}

	// Reassemble the data from all but the last packet
	for _, chunk := range receivedChunks[:len(receivedChunks)-1] {
		reassembledBody = append(reassembledBody, chunk.Data...)
	}

	// 7. Parse the reassembled data as an HTTP response and verify its content.
	reassembledResp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(reassembledBody)), req)
	if err != nil {
		t.Fatalf("Failed to read reassembled HTTP response: %v", err)
	}
	defer reassembledResp.Body.Close()

	finalBody, err := io.ReadAll(reassembledResp.Body)
	if err != nil {
		t.Fatalf("Failed to read reassembled response body: %v", err)
	}

	if reassembledResp.StatusCode != http.StatusOK {
		t.Errorf("Expected status code %d, got %d", http.StatusOK, reassembledResp.StatusCode)
	}

	if !bytes.Equal(finalBody, responseBody) {
		t.Errorf("Reassembled response body does not match original. Got %d bytes, want %d bytes.", len(finalBody), len(responseBody))
	}

	t.Logf("Successfully reassembled %d chunks into a valid HTTP response.", len(packets))
}

