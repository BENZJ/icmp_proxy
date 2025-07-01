package main

import (
	"fmt"
	"icmptun/pkg/protocol"
	"io"
	"log"
	"net"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
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

	fmt.Println("ICMP server started. Waiting for packets...")

	for {
		// Read from the connection
		buf := make([]byte, 1500) // MTU size
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			log.Printf("Error reading from ICMP connection: %v", err)
			continue
		}

		// Parse the ICMP message
		msg, err := icmp.ParseMessage(ipv4.ICMPTypeEcho.Protocol(), buf[:n])
		if err != nil {
			log.Printf("Error parsing ICMP message: %v", err)
			continue
		}

		// Check if it's an Echo request and has our magic ID
		if echo, ok := msg.Body.(*icmp.Echo); ok && msg.Type == ipv4.ICMPTypeEcho && echo.ID == protocol.MagicID {
			log.Printf("Received valid ICMP packet from %s with seq %d", addr, echo.Seq)
			// Handle the packet in a new goroutine to not block the main loop
			go handlePacket(conn, addr, echo)
		}
	}
}

func handlePacket(conn icmpConn, addr net.Addr, req *icmp.Echo) {
	// Forward the data to the target service
	targetConn, err := net.Dial("tcp", protocol.TargetServiceAddr)
	if err != nil {
		log.Printf("Failed to connect to target service '%s': %v", protocol.TargetServiceAddr, err)
		return
	}
	defer targetConn.Close()

	// Write the payload from the ICMP packet to the target service
	if _, err := targetConn.Write(req.Data); err != nil {
		log.Printf("Failed to write to target service: %v", err)
		return
	}

	// Read the response from the target service
	response, err := io.ReadAll(targetConn)
	if err != nil {
		log.Printf("Failed to read response from target service: %v", err)
		return
	}

	// Send the response back to the client, chunked into multiple ICMP packets if necessary.
	// For simplicity, this example sends the response in a single packet.
	// A real-world implementation would need to handle chunking for larger responses.
	reply := &icmp.Message{
		Type: ipv4.ICMPTypeEchoReply,
		Code: 0,
		Body: &icmp.Echo{
			ID:   req.ID,
			Seq:  req.Seq, // Use the same sequence number to match the request
			Data: response,
		},
	}

	rb, err := reply.Marshal(nil)
	if err != nil {
		log.Printf("Failed to marshal ICMP reply: %v", err)
		return
	}

	if _, err := conn.WriteTo(rb, addr); err != nil {
		log.Printf("Failed to write ICMP reply to %s: %v", addr, err)
	} else {
		log.Printf("Sent reply to %s with seq %d", addr, req.Seq)
	}
}