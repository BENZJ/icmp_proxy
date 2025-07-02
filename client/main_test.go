package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strconv"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// packet and mock connection for local testing without raw sockets
type mockPacket struct {
	data []byte
	addr net.Addr
}

type mockPacketConn struct {
	recv chan mockPacket
	peer chan mockPacket
}

func newMockPair() (*mockPacketConn, *mockPacketConn) {
	c2s := make(chan mockPacket, 10)
	s2c := make(chan mockPacket, 10)
	return &mockPacketConn{recv: s2c, peer: c2s}, &mockPacketConn{recv: c2s, peer: s2c}
}

func (m *mockPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	p, ok := <-m.recv
	if !ok {
		return 0, nil, io.EOF
	}
	copy(b, p.data)
	return len(p.data), p.addr, nil
}

func (m *mockPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	if m.peer == nil {
		return 0, io.ErrClosedPipe
	}
	m.peer <- mockPacket{data: append([]byte(nil), b...), addr: addr}
	return len(b), nil
}

func (m *mockPacketConn) Close() error {
	if m.peer != nil {
		close(m.peer)
		m.peer = nil
	}
	return nil
}

// TestClientProxyWorkflow simulates the entire client-side process using a shared connection.
func TestClientProxyWorkflow(t *testing.T) {
	// 1. 使用模拟连接对进行测试，避免需要 root 权限
	clientConn, serverConn := newMockPair()
	icmpConn = clientConn
	defer icmpConn.Close()

	// 2. Start the client's main response listener in the background.
	go listenForICMPResponses()

	// 3. Run the request and response simulation in a separate goroutine using the server side of the pair.
	go simulateRequestAndResponse(t, serverConn)

	// 4. Create a mock HTTP request, as if from a browser.
	requestPayload := "你好，服务器！"
	req := httptest.NewRequest("POST", "http://example.com/foo", bytes.NewBufferString(requestPayload))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Content-Length", strconv.Itoa(len(requestPayload)))
	rr := httptest.NewRecorder()

	// 5. Call the proxy handler. This will trigger the simulation.
	handleHTTPProxyRequest(rr, req)

	// 6. Verify the final response.
	resp := rr.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("期望状态码为 OK (200)，但得到 %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Test-Header") != "true" {
		t.Errorf("期望 'X-Test-Header' 为 'true'，但它缺失或不正确")
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	if string(body) != requestPayload {
		t.Errorf("期望的响应体为 '%s'，但得到 '%s'", requestPayload, string(body))
	}

	t.Log("成功接收并验证了代理的响应。")
}

// simulateRequestAndResponse mimics the server's behavior using the server side connection.
func simulateRequestAndResponse(t *testing.T, conn *mockPacketConn) {
	// Read one packet from the shared connection (the client's request)
	buf := make([]byte, 1500)
	n, addr, err := conn.ReadFrom(buf)
	if err != nil {
		t.Errorf("模拟服务器读取 ICMP 包失败: %v", err)
		return
	}
	t.Logf("服务器收到 %d 字节请求", n)

	msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), buf[:n])
	if err != nil {
		t.Errorf("模拟服务器解析 ICMP 消息失败: %v", err)
		return
	}

	reqEcho, ok := msg.Body.(*icmp.Echo)
	if !ok {
		t.Errorf("模拟服务器收到了非 ECHO 请求")
		return
	}

	// 回复包必须使用请求的 ID 作为会话标识
	responseID := reqEcho.ID

	// Create a fake HTTP response.
	t.Logf("原始请求包:\n%s", string(reqEcho.Data))
	httpReq, _ := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqEcho.Data)))
	reqBody, _ := io.ReadAll(httpReq.Body)
	t.Logf("重建的请求体长度: %d", len(reqBody))
	httpResp := &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(reqBody)),
		ContentLength: int64(len(reqBody)),
	}
	httpResp.Header.Set("Content-Type", "text/plain")
	httpResp.Header.Set("X-Test-Header", "true")
	respBytes, _ := httputil.DumpResponse(httpResp, true)

	// Send the response back in chunks.
	chunk1 := respBytes[:len(respBytes)/2]
	chunk2 := respBytes[len(respBytes)/2:]

	// 响应包需要与请求 ID 匹配，每个分片使用递增的 Seq 编号。
	sendChunk(t, conn, addr, responseID, 0, chunk1)
	sendChunk(t, conn, addr, responseID, 1, chunk2)
	sendChunk(t, conn, addr, responseID, 2, []byte{}) // Final packet
	t.Log("服务器已发送所有分片")
}

func sendChunk(t *testing.T, conn *mockPacketConn, addr net.Addr, id, seq int, data []byte) {
	reply := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,  // 与请求 ID 一致
			Seq:  seq, // 分片序号
			Data: data,
		},
	}
	rb, err := reply.Marshal(nil)
	if err != nil {
		t.Fatalf("封包块 %d 失败: %v", seq, err)
	}
	// Use the global icmpConn to write the response.
	if _, err := conn.WriteTo(rb, addr); err != nil {
		t.Fatalf("写入块 %d 失败: %v", seq, err)
	}
}
