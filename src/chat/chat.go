package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	fmt.Println("To send a file, type '/sendfile <filepath>'")

	// Start a goroutine to receive chat messages and files.
	go func() {
		for {
			data, err := conn.Receive()
			if err != nil {
				log.Errorf("error receiving message: %v", err)
				return
			}
			// assume chat messages are sent as JSON encoded with type "chat"
			var m message.Message
			err = json.Unmarshal(data, &m)
			if err != nil {
				log.Debugf("failed to unmarshal message: %v", err)
				continue
			}
			if m.Type == "chat" {
				fmt.Printf("[Peer]: %s\n", m.Message)
			} else if m.Type == "chatfile" {
				// m.Message holds the file name; m.Bytes holds file contents.
				recvDir := "chat_received_files"
				os.MkdirAll(recvDir, 0755)
				filePath := filepath.Join(recvDir, m.Message)
				err = os.WriteFile(filePath, m.Bytes, 0644)
				if err != nil {
					fmt.Printf("Failed to save file '%s': %v\n", m.Message, err)
				} else {
					fmt.Printf("[Peer] sent file '%s'. Saved to %s\n", m.Message, filePath)
				}
			} else {
				fmt.Printf("[Peer unknown]: %s\n", m.Message)
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
		// If input starts with /sendfile, then send file.
		if strings.HasPrefix(line, "/sendfile ") {
			filePath := strings.TrimSpace(strings.TrimPrefix(line, "/sendfile "))
			// Read file content.
			content, err := os.ReadFile(filePath)
			if err != nil {
				fmt.Printf("Error reading file %s: %v\n", filePath, err)
				continue
			}
			// Extract file name.
			_, fname := filepath.Split(filePath)
			chatFileMsg := message.Message{
				Type:    "chatfile",
				Message: fname,
				Bytes:   content,
			}
			data, err := json.Marshal(chatFileMsg)
			if err != nil {
				log.Errorf("error marshaling file message: %v", err)
				continue
			}
			err = conn.Send(data)
			if err != nil {
				log.Errorf("error sending file message: %v", err)
			}
			fmt.Printf("Sent file '%s'\n", fname)
		} else {
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
	}
	return nil
}
