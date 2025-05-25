package chat

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// New encryption helper functions using AES-GCM.
func encrypt(plainText, key string) (string, error) {
	// Derive 32-byte key from secret.
	hash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	cipherText := aesGCM.Seal(nonce, nonce, []byte(plainText), nil)
	return hex.EncodeToString(cipherText), nil
}

func decrypt(cipherHex, key string) (string, error) {
	cipherText, err := hex.DecodeString(cipherHex)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := aesGCM.NonceSize()
	if len(cipherText) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, cipherText := cipherText[:nonceSize], cipherText[nonceSize:]
	plainText, err := aesGCM.Open(nil, nonce, cipherText, nil)
	if err != nil {
		return "", err
	}
	return string(plainText), nil
}

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

	// Prompt for alias at start.
	var myAlias string
	fmt.Print("Enter your alias: ")
	fmt.Scanln(&myAlias)
	fmt.Printf("Your alias is set to '%s'\n", myAlias)

	// Start a goroutine to receive chat messages and files with reconnection
	go func() {
		for {
			data, err := conn.Receive()
			if err != nil {
				log.Errorf("error receiving message: %v", err)
				fmt.Println("Peer disconnected. Waiting for new connection...")
				// reconnect loop
				for {
					newConn, newBanner, newIp, errReconnect := tcp.ConnectToTCPServer(options.RelayAddress, options.RelayPassword, options.RoomName, 30*time.Second)
					if errReconnect != nil {
						log.Errorf("reconnect failed: %v", errReconnect)
						time.Sleep(5 * time.Second)
						continue
					}
					banner = newBanner
					conn = newConn
					fmt.Printf("Reconnected to chat room '%s' at %s.\n", options.RoomName, newIp)
					break
				}
				continue
			}
			// assume chat messages are sent as JSON encoded with type "chat"
			var m message.Message
			err = json.Unmarshal(data, &m)
			if err != nil {
				log.Debugf("failed to unmarshal message: %v", err)
				continue
			}
			// Display alias on left side.
			alias := m.Alias
			if alias == "" {
				alias = "Peer"
			}
			switch m.Type {
			case "chat":
				fmt.Printf("[%s]: %s\n", alias, m.Message)
			case "chatfile":
				// m.Message holds the file name; m.Bytes holds file contents.
				recvDir := "chat_received_files"
				os.MkdirAll(recvDir, 0755)
				filePath := filepath.Join(recvDir, m.Message)
				err = os.WriteFile(filePath, m.Bytes, 0644)
				if err != nil {
					fmt.Printf("Failed to save file '%s': %v\n", m.Message, err)
				} else {
					fmt.Printf("[%s] sent file '%s'. Saved to %s\n", alias, m.Message, filePath)
				}
			case "encrypted":
				// Prompt for decryption key.
				fmt.Printf("Encrypted message from [%s]. Enter decryption key: ", alias)
				var key string
				fmt.Scanln(&key)
				plain, err := decrypt(m.Message, key)
				if err != nil {
					fmt.Printf("Failed to decrypt message: %v\n", err)
				} else {
					fmt.Printf("[%s]: %s\n", alias, plain)
				}
			default:
				fmt.Printf("[%s unknown]: %s\n", alias, m.Message)
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
		// Allow updating alias.
		if strings.HasPrefix(line, "/setalias ") {
			myAlias = strings.TrimSpace(strings.TrimPrefix(line, "/setalias "))
			fmt.Printf("Alias updated to '%s'\n", myAlias)
			continue
		}
		// Send encrypted message.
		if strings.HasPrefix(line, "/encrypt ") {
			parts := strings.SplitN(line, " ", 3)
			if len(parts) < 3 {
				fmt.Println("Usage: /encrypt <secret> <message>")
				continue
			}
			secret := parts[1]
			plaintext := parts[2]
			cipherText, err := encrypt(plaintext, secret)
			if err != nil {
				fmt.Printf("Encryption error: %v\n", err)
				continue
			}
			encMsg := message.Message{
				Type:    "encrypted",
				Message: cipherText,
				Alias:   myAlias,
			}
			data, err := json.Marshal(encMsg)
			if err != nil {
				log.Errorf("error marshaling encrypted message: %v", err)
				continue
			}
			err = conn.Send(data)
			if err != nil {
				log.Errorf("error sending encrypted message: %v", err)
			}
			continue
		}
		// Send file command.
		if strings.HasPrefix(line, "/sendfile ") {
			filePath := strings.TrimSpace(strings.TrimPrefix(line, "/sendfile "))
			content, err := os.ReadFile(filePath)
			if err != nil {
				fmt.Printf("Error reading file %s: %v\n", filePath, err)
				continue
			}
			_, fname := filepath.Split(filePath)
			chatFileMsg := message.Message{
				Type:    "chatfile",
				Message: fname,
				Bytes:   content,
				Alias:   myAlias,
			}
			data, err := json.Marshal(chatFileMsg)
			if err != nil {
				log.Errorf("error marshaling file message: %v", err)
				continue
			}
			if err = conn.Send(data); err != nil {
				log.Errorf("error sending file message: %v", err)
			}
			fmt.Printf("Sent file '%s'\n", fname)
			continue
		}
		// Otherwise, send standard chat message.
		chatMsg := message.Message{
			Type:    "chat",
			Message: line,
			Alias:   myAlias,
		}
		data, err := json.Marshal(chatMsg)
		if err != nil {
			log.Errorf("error marshaling chat message: %v", err)
			continue
		}
		if err = conn.Send(data); err != nil {
			log.Errorf("error sending chat message: %v", err)
			continue
		}
	}
	return nil
}
