package main

import (
	"bufio"
	"bytes"
	"fmt"
	"icmptun/pkg/protocol"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	// MaxChunkSize defines the maximum size of data in a single ICMP packet.
	// We leave some space for IP and ICMP headers.
	MaxChunkSize = 1400
)

// icmpConn defines the interface for an ICMP connection that can write packets.
// This allows for mocking in tests.
type icmpConn interface {
	WriteTo(b []byte, addr net.Addr) (int, error)
}

func main() {
	// Start listening for ICMP packets. This requires root privileges.
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Fatalf("Error listening for ICMP packets: %v. Note: this may require root privileges.", err)
	}
	defer conn.Close()

	fmt.Println("ICMP HTTP Proxy server started. Waiting for requests...")

	for {
		buf := make([]byte, 1500) // MTU size
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			log.Printf("Error reading from ICMP connection: %v", err)
			continue
		}

		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), buf[:n])
		if err != nil {
			log.Printf("Error parsing ICMP message: %v", err)
			continue
		}

		// We no longer check for a magic ID. Any valid Echo request is a potential tunnel packet.
		// The client is responsible for generating a unique ID for each request.
		if echo, ok := msg.Body.(*icmp.Echo); ok && msg.Type == ipv4.ICMPTypeEcho {
			log.Printf("Received ICMP request from %s with ID %d, length %d", addr, echo.ID, len(echo.Data))
			go handleHttpRequest(conn, addr, echo)
		}
	}
}

// handleHttpRequest parses the ICMP data as an HTTP request, executes it, and sends back the response.
func handleHttpRequest(conn icmpConn, addr net.Addr, reqPacket *icmp.Echo) {
	// 1. Parse the data from the ICMP packet as an HTTP request.
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(reqPacket.Data)))
	if err != nil {
		log.Printf("Error reading HTTP request from ICMP data: %v", err)
		return
	}
	log.Printf("Proxying request for %s %s", req.Method, req.URL)

	// This is the crucial fix: The Go HTTP client requires RequestURI to be empty.
	req.RequestURI = ""

	// Reconstruct the URL for the client.
	// For now, we'll assume HTTP. A more robust proxy would handle HTTPS.
	req.URL.Scheme = "http"
	req.URL.Host = req.Host

	// 2. Execute the HTTP request.
	// We use a standard HTTP client. It will handle DNS lookups, connections, etc.
	client := &http.Client{
		// Set a reasonable timeout.
		Timeout: 30 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to execute HTTP request to %s: %v", req.Host, err)
		// TODO: Send an error response back to the client.
		return
	}
	defer resp.Body.Close()

	// 3. Dump the entire HTTP response (status line, headers, body) into a byte slice.
	respBytes, err := httputil.DumpResponse(resp, true)
	if err != nil {
		log.Printf("Failed to dump HTTP response: %v", err)
		return
	}

	// 4. Send the response back to the client, chunked into multiple ICMP packets.
	sendResponseInChunks(conn, addr, reqPacket.ID, respBytes)
}

// sendResponseInChunks splits a large response into smaller chunks and sends them as a series of ICMP packets.
func sendResponseInChunks(conn icmpConn, addr net.Addr, requestID int, data []byte) {
	totalLen := len(data)
	log.Printf("Sending response of %d bytes to %s in chunks.", totalLen, addr)

	for seq, i := 0, 0; i < totalLen; i, seq = i+MaxChunkSize, seq+1 {
		end := i + MaxChunkSize
		if end > totalLen {
			end = totalLen
		}
		chunk := data[i:end]

		reply := &icmp.Message{
			Type: ipv4.ICMPTypeEchoReply,
			Code: 0,
			Body: &icmp.Echo{
				ID:   requestID, // Use the same ID for all chunks of a response
				Seq:  seq,       // Use sequence number to order chunks
				Data: chunk,
			},
		}

		rb, err := reply.Marshal(nil)
		if err != nil {
			log.Printf("Failed to marshal ICMP reply chunk #%d: %v", seq, err)
			return // Stop if we can't marshal a chunk
		}

		if _, err := conn.WriteTo(rb, addr); err != nil {
			log.Printf("Failed to write ICMP reply chunk #%d to %s: %v", seq, addr, err)
			return // Stop if we can't write a chunk
		}
	}

	// After sending all data chunks, send a final, zero-length packet to signal the end of the response.
	finalPacket := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{
			ID:   requestID,
			Seq:  len(data)/MaxChunkSize + 1, // Sequence number after the last data chunk
			Data: []byte{},
		},
	}
	fb, err := finalPacket.Marshal(nil)
	if err != nil {
		log.Printf("Failed to marshal final ICMP packet: %v", err)
		return
	}
	if _, err := conn.WriteTo(fb, addr); err != nil {
		log.Printf("Failed to write final ICMP packet to %s: %v", addr, err)
	} else {
		log.Printf("Finished sending response to %s.", addr)
	}
}
