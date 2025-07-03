package main

import (
	"bufio"
	"bytes"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	// MaxChunkSize 定义一个 ICMP 包内的最大数据尺寸，保留给 IP 和 ICMP 头的空间
	MaxChunkSize = 1400
	// ResponseSeqStart 指定服务器发送响应分片时使用的起始序号。
	// 保留 0 用于区分系统自动产生的 ping 响应。
	ResponseSeqStart = 1
)

// icmpConn 定义一个可以写入 ICMP 包的接口，主要使用于单元测试时的模拟
type icmpConn interface {
	WriteTo(b []byte, addr net.Addr) (int, error)
}

func main() {
	// 启动监听 ICMP 包，通常需要 root 权限
	log.Printf("开始监听 ICMP network=%s address=%s", "ip4:icmp", "0.0.0.0")
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Fatalf("Error listening for ICMP packets: %v. Note: this may require root privileges.", err)
	}
	defer func() {
		conn.Close()
		log.Println("ICMP 监听器已关闭")
	}()

	log.Println("ICMP HTTP 代理服务器已启动，等待请求...")

	for {
		buf := make([]byte, 1500) // MTU 大小
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			log.Printf("读取 ICMP 连接数据失败: %v", err)
			continue
		}

		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), buf[:n])
		if err != nil {
			log.Printf("解析 ICMP 消息失败: %v", err)
			continue
		}

		// 这里不再检查特殊的 ID，任何 Echo 请求都视作隧道数据，由客户端保证 ID 唯一
		if echo, ok := msg.Body.(*icmp.Echo); ok && msg.Type == ipv4.ICMPTypeEcho {
			log.Printf("收到来自 %s 的 ICMP 请求，ID %d，长度 %d", addr, echo.ID, len(echo.Data))
			go handleHttpRequest(conn, addr, echo)
		}
	}
}

// handleHttpRequest 将 ICMP 数据解析成 HTTP 请求，执行后把响应返回给客户端
func handleHttpRequest(conn icmpConn, addr net.Addr, reqPacket *icmp.Echo) {
	// 步骤1：将 ICMP 数据解析为 HTTP 请求
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqPacket.Data)))
	if err != nil {
		log.Printf("解析 ICMP 数据为 HTTP 请求失败: %v", err)
		return
	}
	log.Printf("转发 %s %s", req.Method, req.URL)

	// Go 的 HTTP 客户端要求 RequestURI 为空
	req.RequestURI = ""

	// 重新构造 URL，目前仅处理 HTTP，实际应用中应考虑 HTTPS
	req.URL.Scheme = "http"
	req.URL.Host = req.Host

	// 步骤2：执行 HTTP 请求，使用标准客户端处理 DNS 和连接等
	client := &http.Client{
		// 设置超时时间
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("执行 HTTP 请求到 %s 失败: %v", req.Host, err)
		// TODO: 可在此处将错误信息回传给客户端
		return
	}
	defer resp.Body.Close()

	// 步骤3：将完整的 HTTP 响应（状态行、头、体）转为字节
	respBytes, err := httputil.DumpResponse(resp, true)
	if err != nil {
		log.Printf("转储 HTTP 响应失败: %v", err)
		return
	}

	// 步骤4：把响应按块拆分，以 ICMP 包发送给客户端
	sendResponseInChunks(conn, addr, reqPacket.ID, respBytes)
}

// sendResponseInChunks 将大响应拆分成多个 ICMP 包顺序发送
func sendResponseInChunks(conn icmpConn, addr net.Addr, requestID int, data []byte) {
	totalLen := len(data)
	log.Printf("以分块形式向 %s 发送 %d 字节响应", addr, totalLen)

	for seq, i := ResponseSeqStart, 0; i < totalLen; i, seq = i+MaxChunkSize, seq+1 {
		end := i + MaxChunkSize
		if end > totalLen {
			end = totalLen
		}
		chunk := data[i:end]

		reply := &icmp.Message{
			Type: ipv4.ICMPTypeEchoReply,
			Code: 0,
			Body: &icmp.Echo{
				ID:   requestID, // 所有分片使用同一 ID
				Seq:  seq,       // 序号用于重组顺序
				Data: chunk,
			},
		}

		rb, err := reply.Marshal(nil)
		if err != nil {
			log.Printf("编码第 %d 个 ICMP 响应分片失败: %v", seq, err)
			return // 编码失败则停止发送
		}

		if _, err := conn.WriteTo(rb, addr); err != nil {
			log.Printf("发送 ICMP 响应分片 #%d 到 %s 失败: %v", seq, addr, err)
			return // 发送失败则停止
		}
	}

	// 所有数据发送完毕后，再发一个零长度包表示结束
	finalPacket := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{
			ID:   requestID,
			Seq:  ResponseSeqStart + (len(data)+MaxChunkSize-1)/MaxChunkSize,
			Data: []byte{},
		},
	}
	fb, err := finalPacket.Marshal(nil)
	if err != nil {
		log.Printf("最终 ICMP 包编码失败: %v", err)
		return
	}
	if _, err := conn.WriteTo(fb, addr); err != nil {
		log.Printf("发送最终 ICMP 包到 %s 失败: %v", addr, err)
	} else {
		log.Printf("完成向 %s 发送响应", addr)
	}
}
