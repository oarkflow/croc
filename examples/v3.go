package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings" // added import
	"sync"    // ...existing import...

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	relay "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: v3 [relay|host|join] [hostMultiaddr (if join)]")
		return
	}
	mode := os.Args[1]
	switch mode {
	case "relay":
		if err := runRelay(); err != nil {
			log.Fatal(err)
		}
	case "host":
		if err := runHostChat(); err != nil {
			log.Fatal(err)
		}
	case "join":
		if len(os.Args) < 3 {
			fmt.Println("Usage for join: v3 join <hostMultiaddr>")
			return
		}
		hostAddr := os.Args[2]
		if err := runJoinChat(hostAddr); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Println("Unknown mode")
	}
}

func runRelay() error {
	// Create a relay node
	relayHost, err := libp2p.New()
	if err != nil {
		return fmt.Errorf("Failed to create relay host: %w", err)
	}
	if _, err := relay.New(relayHost); err != nil {
		return fmt.Errorf("Failed to instantiate relay: %w", err)
	}
	log.Println("Relay node running. Addresses:")
	for _, addr := range relayHost.Addrs() {
		log.Printf("%s/p2p/%s", addr, relayHost.ID().String())
	}
	// Keep the process alive
	select {}
}

func runHostChat() error {
	// Prompt for host alias
	fmt.Print("Enter your alias: ")
	reader := bufio.NewReader(os.Stdin)
	hostAlias, _ := reader.ReadString('\n')
	hostAlias = strings.TrimSpace(hostAlias)

	// Create a host with relay support enabled
	host, err := libp2p.New(libp2p.EnableRelay())
	if err != nil {
		return fmt.Errorf("Failed to create chat host: %w", err)
	}
	// Slices and mutex for streams and alias mapping
	var hostStreams []network.Stream
	var streamsMutex sync.Mutex
	aliasMap := make(map[peer.ID]string)
	var aliasMutex sync.Mutex
	chatCh := make(chan string)

	// Broadcast goroutine: prints locally and sends messages to all connected streams
	go func() {
		for msg := range chatCh {
			fmt.Println("\r" + msg)
			streamsMutex.Lock()
			for _, s := range hostStreams {
				w := bufio.NewWriter(s)
				w.WriteString(msg + "\n")
				w.Flush()
			}
			streamsMutex.Unlock()
		}
	}()

	// Set stream handler for chat with a handshake to receive alias from new peer.
	host.SetStreamHandler("/chat/1.0.0", func(s network.Stream) {
		log.Println("New chat connection from", s.Conn().RemotePeer().String())
		streamsMutex.Lock()
		hostStreams = append(hostStreams, s)
		streamsMutex.Unlock()
		go func() {
			r := bufio.NewReader(s)
			scanner := bufio.NewScanner(r)
			var peerAlias string
			if scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "ALIAS: ") {
					peerAlias = strings.TrimSpace(line[7:])
					aliasMutex.Lock()
					aliasMap[s.Conn().RemotePeer()] = peerAlias
					aliasMutex.Unlock()
					log.Println("Set alias for", s.Conn().RemotePeer().String(), "as", peerAlias)
				}
			}
			for scanner.Scan() {
				chatCh <- fmt.Sprintf("%s: %s", peerAlias, scanner.Text())
			}
		}()
	})

	log.Println("Chat room hosted. Unique room code:", host.ID().String())
	log.Println("Your alias:", hostAlias)
	for _, addr := range host.Addrs() {
		log.Printf("%s/p2p/%s", addr, host.ID().String())
	}
	// Host's input loop – using alias for broadcast
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}
		chatCh <- hostAlias + ": " + scanner.Text()
	}
	return nil
}

func runJoinChat(hostMultiaddr string) error {
	ctx := context.Background()
	// Prompt for joiner alias
	fmt.Print("Enter your alias: ")
	reader := bufio.NewReader(os.Stdin)
	myAlias, _ := reader.ReadString('\n')
	myAlias = strings.TrimSpace(myAlias)

	// Create a host with relay support enabled.
	peerHost, err := libp2p.New(libp2p.EnableRelay())
	if err != nil {
		return fmt.Errorf("Failed to create peer host: %w", err)
	}
	maddr, err := ma.NewMultiaddr(hostMultiaddr)
	if err != nil {
		return fmt.Errorf("Invalid multiaddr: %w", err)
	}
	pi, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("Failed to extract peer info: %w", err)
	}
	if err := peerHost.Connect(ctx, *pi); err != nil {
		return fmt.Errorf("Connection failed: %w", err)
	}
	log.Println("Connected to chat host:", pi.ID.String())
	stream, err := peerHost.NewStream(ctx, pi.ID, "/chat/1.0.0")
	if err != nil {
		return fmt.Errorf("Failed to open chat stream: %w", err)
	}
	rw := bufio.NewReadWriter(bufio.NewReader(stream), bufio.NewWriter(stream))
	// Send alias handshake to the host.
	rw.WriteString("ALIAS: " + myAlias + "\n")
	rw.Flush()
	// Read incoming messages
	go func() {
		scanner := bufio.NewScanner(rw)
		for scanner.Scan() {
			log.Println(scanner.Text())
		}
	}()
	// Read user input and send messages (alias already provided)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("You: ")
		if !scanner.Scan() {
			break
		}
		text := scanner.Text()
		_, err := rw.WriteString(text + "\n")
		if err != nil {
			return fmt.Errorf("Failed to write to stream: %w", err)
		}
		rw.Flush()
	}
	return nil
}
