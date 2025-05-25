package chat

import (
	"bufio"
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

// ANSI color codes.
const (
	ResetColor   = "\033[0m"
	BlueColor    = "\033[34m"
	GreenColor   = "\033[32m"
	YellowColor  = "\033[33m"
	MagentaColor = "\033[35m"
	CyanColor    = "\033[36m"
)

// Helper to wrap text in color.
func colorText(text, color string) string {
	return fmt.Sprintf("%s%s%s", color, text, ResetColor)
}

// Helper to get current timestamp.
func timestamp() string {
	return colorText(time.Now().Format("15:04:05"), YellowColor)
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
	fmt.Printf("Your alias is set to '%s'\n", colorText(myAlias, GreenColor))

	// Setup readline with a fancy dynamic prompt.
	rlPrompt := fmt.Sprintf("%s %s> ", timestamp(), colorText(myAlias, GreenColor))
	rl, err := readline.NewEx(&readline.Config{
		Prompt: rlPrompt,
	})
	if err != nil {
		return err
	}
	defer rl.Close()

	// Start a goroutine to receive chat messages and files with reconnection
	go func() {
		for {
			data, err := conn.Receive()
			if err != nil {
				log.Errorf("error receiving message: %v", err)
				rl.Write([]byte("\nPeer disconnected. Waiting for new connection...\n"))
				rl.Refresh()
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
					rl.Write([]byte(fmt.Sprintf("\nReconnected to chat room '%s' at %s.\n", options.RoomName, newIp)))
					rl.Refresh()
					break
				}
				continue
			}
			var m message.Message
			err = json.Unmarshal(data, &m)
			if err != nil {
				log.Debugf("failed to unmarshal message: %v", err)
				continue
			}
			alias := m.Alias
			if alias == "" {
				alias = "Peer"
			}
			switch m.Type {
			case "chat":
				msg := fmt.Sprintf("%s [%s]: %s", timestamp(), colorText(alias, BlueColor), m.Message)
				rl.Write([]byte("\n" + msg + "\n"))
				rl.Refresh()
			case "chatfile":
				// Using bufio to prompt for file acceptance and save location.
				reader := bufio.NewReader(os.Stdin)
				rl.Write([]byte(fmt.Sprintf("\n%s [%s] wants to send file '%s'. Accept file? (yes/no): ", timestamp(), colorText(alias, BlueColor), m.Message)))
				rl.Refresh()
				resp, _ := reader.ReadString('\n')
				resp = strings.TrimSpace(resp)
				if strings.ToLower(resp) != "yes" {
					rl.Write([]byte("File transfer declined.\n"))
					rl.Refresh()
					continue
				}
				rl.Write([]byte("Enter directory to save file: "))
				rl.Refresh()
				saveDir, _ := reader.ReadString('\n')
				saveDir = strings.TrimSpace(saveDir)
				if saveDir == "" {
					saveDir = "chat_received_files"
				}
				os.MkdirAll(saveDir, 0755)
				filePath := filepath.Join(saveDir, m.Message)
				err = os.WriteFile(filePath, m.Bytes, 0644)
				if err != nil {
					rl.Write([]byte(fmt.Sprintf("Failed to save file '%s': %v\n", m.Message, err)))
				} else {
					rl.Write([]byte(fmt.Sprintf("%s [%s] sent file '%s'. Saved to %s\n", timestamp(), colorText(alias, BlueColor), m.Message, filePath)))
				}
				rl.Refresh()
			case "encrypted":
				reader := bufio.NewReader(os.Stdin)
				rl.Write([]byte(fmt.Sprintf("\n%s Encrypted message from [%s]. Enter decryption key: ", timestamp(), colorText(alias, BlueColor))))
				rl.Refresh()
				key, _ := reader.ReadString('\n')
				key = strings.TrimSpace(key)
				plain, err := decrypt(m.Message, key)
				if err != nil {
					rl.Write([]byte(fmt.Sprintf("Failed to decrypt message: %v\n", err)))
				} else {
					rl.Write([]byte(fmt.Sprintf("%s [%s]: %s\n", timestamp(), colorText(alias, BlueColor), plain)))
				}
				rl.Refresh()
			default:
				msg := fmt.Sprintf("%s [%s unknown]: %s", timestamp(), colorText(alias, BlueColor), m.Message)
				rl.Write([]byte("\n" + msg + "\n"))
				rl.Refresh()
			}
		}
	}()

	// Chat input loop with dynamic prompt update.
	for {
		rl.SetPrompt(fmt.Sprintf("%s %s> ", timestamp(), colorText(myAlias, GreenColor)))
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
			fmt.Printf("Alias updated to '%s'\n", colorText(myAlias, GreenColor))
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
			if err = conn.Send(data); err != nil {
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
