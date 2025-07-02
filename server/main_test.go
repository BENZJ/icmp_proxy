package main

import (
	"bufio"
	"bytes"
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

// mockIcmpConn 用于在测试中捕获写入的 ICMP 数据包
type mockIcmpConn struct {
	mu      sync.Mutex
	packets [][]byte
	addr    net.Addr
}

// WriteTo 实现 icmpConn 接口，保存写入的数据包
func (m *mockIcmpConn) WriteTo(p []byte, addr net.Addr) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 保存数据包的副本
	packetCopy := make([]byte, len(p))
	copy(packetCopy, p)
	m.packets = append(m.packets, packetCopy)
	m.addr = addr
	return len(p), nil
}

// GetPackets 返回捕获的数据包副本，便于检查
func (m *mockIcmpConn) GetPackets() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.packets...)
}

// TestHandleHttpRequest_Chunking 测试完整的代理逻辑以及分片发送
func TestHandleHttpRequest_Chunking(t *testing.T) {
	// 1. 构建返回大量数据的模拟 HTTP 服务
	responseBody := bytes.Repeat([]byte("0123456789"), 300) // 总计 3000 字节
	mockHTTPServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write(responseBody)
	}))
	defer mockHTTPServer.Close()

	// 2. 构造合法的 HTTP GET 请求并转为字节
	req, err := http.NewRequest("GET", mockHTTPServer.URL, nil)
	if err != nil {
		t.Fatalf("Failed to create HTTP request: %v", err)
	}
	reqBytes, err := httputil.DumpRequest(req, true)
	if err != nil {
		t.Fatalf("Failed to dump HTTP request: %v", err)
	}

	// 3. 构造进入服务端的 ICMP 包
	clientAddr := &net.IPAddr{IP: net.ParseIP("127.0.0.1")}
	requestID := 1234
	requestPacket := &icmp.Echo{
		ID:   requestID,
		Seq:  1, // 请求包本身的序号无关紧要
		Data: reqBytes,
	}

	// 4. 创建模拟的 ICMP 连接并调用处理函数
	mockConn := &mockIcmpConn{}
	handleHttpRequest(mockConn, clientAddr, requestPacket)

	// 5. 等待处理函数完成所有分片发送
	time.Sleep(200 * time.Millisecond)

	// 6. 验证结果
	packets := mockConn.GetPackets()
	if len(packets) < 3 { // Should be at least 2 data chunks + 1 final chunk
		t.Fatalf("Expected at least 3 packets for a chunked response, but got %d", len(packets))
	}

	// 将所有分片重新组装为完整响应
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
		if echo.ID != requestID {
			t.Errorf("Packet #%d: expected ID %d, got %d", i, requestID, echo.ID)
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

	// 去掉最后一个空包后重新拼接数据
	for _, chunk := range receivedChunks[:len(receivedChunks)-1] {
		reassembledBody = append(reassembledBody, chunk.Data...)
	}

	// 7. 将重组后的数据解析为 HTTP 响应并校验内容
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
