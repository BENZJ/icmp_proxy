package main

import (
	"bufio"
	"bytes"
	"fmt"
	"icmptun/pkg/protocol"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// packetConn 抽象了我们需要的最小连接接口，便于在测试中替换实现。
type packetConn interface {
	ReadFrom([]byte) (int, net.Addr, error)
	WriteTo([]byte, net.Addr) (int, error)
	Close() error
}

// responseMap safely stores and retrieves response channels for concurrent requests.
type responseMap struct {
	sync.RWMutex
	m map[int]chan *icmp.Echo
}

func (r *responseMap) Get(id int) (chan *icmp.Echo, bool) {
	r.RLock()
	defer r.RUnlock()
	ch, ok := r.m[id]
	return ch, ok
}

func (r *responseMap) Set(id int, ch chan *icmp.Echo) {
	r.Lock()
	defer r.Unlock()
	r.m[id] = ch
}

func (r *responseMap) Delete(id int) {
	r.Lock()
	defer r.Unlock()
	delete(r.m, id)
}

var (
	respChannels = &responseMap{m: make(map[int]chan *icmp.Echo)}
	// Global shared ICMP connection. Using a minimal interface
	// so tests can provide a mock implementation.
	icmpConn packetConn
)

func main() {
	var err error
	// Initialize the global ICMP connection.
	icmpConn, err = icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Fatalf("严重错误: 监听 ICMP 失败: %v. (可能需要 root 权限)", err)
	}
	defer icmpConn.Close()

	// Start the ICMP response listener in the background.
	go listenForICMPResponses()

	// Start the local HTTP proxy server.
	http.HandleFunc("/", handleHTTPProxyRequest)
	log.Printf("HTTP 代理已在 %s 启动", protocol.LocalProxyAddr)
	log.Printf("请将您的浏览器或系统配置使用 HTTP 代理: %s", protocol.LocalProxyAddr)
	if err := http.ListenAndServe(protocol.LocalProxyAddr, nil); err != nil {
		log.Fatalf("启动 HTTP 代理失败: %v", err)
	}
}

// handleHTTPProxyRequest is the handler for our local HTTP proxy.
func handleHTTPProxyRequest(w http.ResponseWriter, r *http.Request) {
	log.Printf("代理请求: %s %s", r.Method, r.URL)

	reqBytes, err := httputil.DumpRequest(r, true)
	if err != nil {
		http.Error(w, "请求转储失败", http.StatusInternalServerError)
		return
	}

	// ICMP ID 字段只有 16 位，因此我们只取时间戳的低 16 位作为请求 ID
	requestID := int(time.Now().UnixNano() & 0xffff)
	respBytes, err := sendICMPRequest(requestID, reqBytes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	log.Printf("收到 %d 字节的代理响应", len(respBytes))
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(respBytes)), r)
	if err != nil {
		http.Error(w, "解析服务器响应失败", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// sendICMPRequest sends data using the global connection.
func sendICMPRequest(requestID int, data []byte) ([]byte, error) {
	dst, err := net.ResolveIPAddr("ip4", protocol.ServerAddr)
	if err != nil {
		return nil, fmt.Errorf("解析服务器地址失败: %w", err)
	}

	ch := make(chan *icmp.Echo, 100)
	respChannels.Set(requestID, ch)
	defer respChannels.Delete(requestID)

	msg := &icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   requestID, // Use the unique request ID as the session identifier.
			Seq:  0,         // Sequence for the request itself is 0.
			Data: data,
		},
	}
	msgBytes, err := msg.Marshal(nil)
	if err != nil {
		return nil, fmt.Errorf("ICMP 请求封包失败: %w", err)
	}
	if _, err := icmpConn.WriteTo(msgBytes, dst); err != nil {
		return nil, fmt.Errorf("ICMP 请求写入失败: %w", err)
	}

	var responsePackets []*icmp.Echo
	timeout := time.After(30 * time.Second)
	for {
		select {
		case packet := <-ch:
			// 系统自动对 ping 请求的回应通常使用与请求相同的序号 0，
			// 为避免误将其当作服务器响应，这里直接忽略 Seq 为 0 的分片。
			if packet.Seq == 0 {
				continue
			}
			if len(packet.Data) == 0 {
				log.Printf("请求 %d 的响应接收完毕", requestID)
				// Sort packets by sequence number before joining
				sort.Slice(responsePackets, func(i, j int) bool {
					return responsePackets[i].Seq < responsePackets[j].Seq
				})
				// Join the data from the sorted packets
				var responseChunks [][]byte
				for _, p := range responsePackets {
					responseChunks = append(responseChunks, p.Data)
				}
				return bytes.Join(responseChunks, nil), nil
			}
			responsePackets = append(responsePackets, packet)
		case <-timeout:
			return nil, fmt.Errorf("请求 %d 超时", requestID)
		}
	}
}

// listenForICMPResponses uses the global connection.
func listenForICMPResponses() {
	for {
		buf := make([]byte, 1500)
		n, addr, err := icmpConn.ReadFrom(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && !netErr.Temporary() {
				log.Printf("ICMP 监听器关闭: %v", err)
				break
			}
			log.Printf("从 ICMP (监听器) 读取错误: %v", err)
			continue
		}

		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEchoReply.Protocol(), buf[:n])
		if err != nil {
			continue
		}

		if reply, ok := msg.Body.(*icmp.Echo); ok && msg.Type == ipv4.ICMPTypeEchoReply {
			log.Printf("收到来自 %s 的响应包 ID=%d Seq=%d 长度=%d", addr, reply.ID, reply.Seq, len(reply.Data))
			if ch, found := respChannels.Get(reply.ID); found {
				ch <- reply
			}
		}
	}
}
