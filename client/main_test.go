package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"testing"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// TestClientProxyWorkflow simulates the entire client-side process using a shared connection.
func TestClientProxyWorkflow(t *testing.T) {
	// 1. Initialize a single, shared ICMP connection for the test.
	var err error
	icmpConn, err = icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		t.Fatalf("测试初始化失败: 无法监听 ICMP: %v. (请使用 'setcap' 或 root 权限运行测试)", err)
	}
	defer icmpConn.Close()

	// 2. Start the client's main response listener in the background.
	go listenForICMPResponses()

	// 3. Run the request and response simulation in a separate goroutine.
	go simulateRequestAndResponse(t)

	// 4. Create a mock HTTP request, as if from a browser.
	requestPayload := "你好，服务器！"
	req := httptest.NewRequest("GET", "http://example.com/foo", bytes.NewBufferString(requestPayload))
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

// simulateRequestAndResponse mimics the server's behavior on the shared connection.
func simulateRequestAndResponse(t *testing.T) {
	// Read one packet from the shared connection (the client's request)
	buf := make([]byte, 1500)
	n, addr, err := icmpConn.ReadFrom(buf)
	if err != nil {
		t.Errorf("模拟服务器读取 ICMP 包失败: %v", err)
		return
	}

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

	// The client now uses the packet's Seq to identify the request session.
	responseID := reqEcho.Seq

	// Create a fake HTTP response.
	httpReq, _ := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqEcho.Data)))
	reqBody, _ := io.ReadAll(httpReq.Body)
	httpResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(reqBody)),
	}
	httpResp.Header.Set("Content-Type", "text/plain")
	httpResp.Header.Set("X-Test-Header", "true")
	respBytes, _ := httputil.DumpResponse(httpResp, true)

	// Send the response back in chunks.
	chunk1 := respBytes[:len(respBytes)/2]
	chunk2 := respBytes[len(respBytes)/2:]

	// The server uses the request's Seq as the response ID (in the Body.ID field)
	// and a new sequence for each chunk (in the Body.Seq field).
	// NOTE: The client code was updated to use reply.ID for session matching.
	// Let's send the response with the correct IDs.
	sendChunk(t, addr, responseID, 0, chunk1)
	sendChunk(t, addr, responseID, 1, chunk2)
	sendChunk(t, addr, responseID, 2, []byte{}) // Final packet
}

func sendChunk(t *testing.T, addr net.Addr, id, seq int, data []byte) {
	reply := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{
			ID:   id,  // The ID of the response should match the request's Seq.
			Seq:  seq, // The sequence number of the chunk.
			Data: data,
		},
	}
	rb, err := reply.Marshal(nil)
	if err != nil {
		t.Fatalf("封包块 %d 失败: %v", seq, err)
	}
	// Use the global icmpConn to write the response.
	if _, err := icmpConn.WriteTo(rb, addr); err != nil {
		t.Fatalf("写入块 %d 失败: %v", seq, err)
	}
}
