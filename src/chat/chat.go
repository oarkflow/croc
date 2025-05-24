package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/chzyer/readline"
	"github.com/schollz/cli/v2"
	"github.com/schollz/croc/v10/src/croc"
	"github.com/schollz/croc/v10/src/message"
	"github.com/schollz/croc/v10/src/tcp"
	log "github.com/schollz/logger"
)

// StartChat initiates a chat session using the given shared code.
// It uses a relay connection (configured via the croc options) and creates a room
// based solely on the shared code.
func StartChat(cCtx *cli.Context, code string) error {
	// For chat sessions, build options with IsChat true.
	options := croc.Options{
		SharedSecret:  code,
		Debug:         cCtx.Bool("debug"),
		RelayAddress:  cCtx.String("relay"),
		RelayAddress6: cCtx.String("relay6"),
		RelayPassword: cCtx.String("pass"),
		IsChat:        true,
	}
	// Validate secret.
	if len(options.SharedSecret) < 4 {
		return fmt.Errorf("code is too short")
	}
	// Compute room name using the full shared secret.
	hashExtra := "croc"
	roomNameBytes := sha256.Sum256([]byte(options.SharedSecret + hashExtra))
	options.RoomName = hex.EncodeToString(roomNameBytes[:])

	// Connect to the relay using the room name.
	// Here we assume the relay is already running.
	conn, banner, ip, err := tcp.ConnectToTCPServer(options.RelayAddress, options.RelayPassword, options.RoomName, 30*time.Second)
	if err != nil {
		return err
	}
	log.Debugf("chat connection established: banner='%s', externalIP=%s", banner, ip)
	fmt.Printf("Joined chat room '%s'. Type your messages and press enter to send.\n", options.RoomName)

	// Start a goroutine to receive chat messages
	go func() {
		for {
			data, err := conn.Receive()
			if err != nil {
				log.Errorf("error receiving chat message: %v", err)
				return
			}
			// assume chat messages are sent as JSON encoded with type "chat"
			var m message.Message
			err = json.Unmarshal(data, &m)
			if err != nil {
				log.Debugf("failed to unmarshal chat message: %v", err)
				continue
			}
			if m.Type == "chat" {
				fmt.Printf("[Peer]: %s\n", m.Message)
			}
		}
	}()

	// Use readline to get input from the keyboard.
	rl, err := readline.NewEx(&readline.Config{
		Prompt: "> ",
	})
	if err != nil {
		return err
	}
	defer rl.Close()

	// Loop: read a line and send it as a chat message.
	for {
		line, err := rl.Readline()
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		chatMsg := message.Message{
			Type:    "chat",
			Message: line,
		}
		data, err := json.Marshal(chatMsg)
		if err != nil {
			log.Errorf("error marshaling chat message: %v", err)
			continue
		}
		err = conn.Send(data)
		if err != nil {
			log.Errorf("error sending chat message: %v", err)
			continue
		}
	}
	return nil
}
