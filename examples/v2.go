package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// Global slice to track ffmpeg processes
var ffmpegProcs []*exec.Cmd

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func readMultilineInput(prompt string) string {
	fmt.Println(prompt)
	fmt.Println("(End input with an empty line)")
	scanner := bufio.NewScanner(os.Stdin)
	var inputLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			break
		}
		inputLines = append(inputLines, line)
	}
	return strings.Join(inputLines, "")
}

func encode(desc *webrtc.SessionDescription) (string, error) {
	// Using JSON marshal for SessionDescription
	b, err := json.Marshal(desc)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func decode(b64 string) (*webrtc.SessionDescription, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	var desc webrtc.SessionDescription
	err = json.Unmarshal(data, &desc)
	if err != nil {
		return nil, err
	}
	return &desc, nil
}

func startFFmpegToPipe(track *webrtc.TrackLocalStaticSample, kind string) {
	var cmd *exec.Cmd

	if runtime.GOOS == "darwin" {
		if kind == "video" {
			// MacOS webcam: device "0:none" (video only)
			cmd = exec.Command("ffmpeg", "-f", "avfoundation", "-framerate", "30", "-i", "0:none", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1")
		} else if kind == "audio" {
			// MacOS mic: device "none:0" (audio only)
			cmd = exec.Command("ffmpeg", "-f", "avfoundation", "-i", "none:0", "-ac", "1", "-ar", "48000", "-f", "s16le", "pipe:1")
		}
	} else {
		if kind == "video" {
			// Linux webcam
			cmd = exec.Command("ffmpeg", "-f", "v4l2", "-i", "/dev/video0", "-pix_fmt", "yuv420p", "-f", "rawvideo", "pipe:1")
		} else if kind == "audio" {
			// Linux mic
			cmd = exec.Command("ffmpeg", "-f", "alsa", "-i", "default", "-ac", "1", "-ar", "48000", "-f", "s16le", "pipe:1")
		}
	}

	stdout, err := cmd.StdoutPipe()
	must(err)
	cmd.Stderr = os.Stderr

	must(cmd.Start())

	// Save the process so it can be killed on exit.
	ffmpegProcs = append(ffmpegProcs, cmd)

	buf := make([]byte, 1400)
	go func() {
		for {
			n, err := stdout.Read(buf)
			if err != nil {
				break
			}
			// using media.Sample instead of webrtc.Sample
			track.WriteSample(media.Sample{Data: buf[:n], Duration: time.Second / 30})
		}
	}()
}

func main() {
	role := flag.String("role", "offer", "offer or answer")
	flag.Parse()

	if *role != "offer" && *role != "answer" {
		log.Fatal("You must specify -role=offer or -role=answer")
	}

	// INITIAL SIGNALING (without media)
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	must(err)

	if *role == "offer" {
		offer, err := peerConnection.CreateOffer(nil)
		must(err)
		must(peerConnection.SetLocalDescription(offer))
		for peerConnection.ICEGatheringState() != webrtc.ICEGatheringStateComplete {
			time.Sleep(100 * time.Millisecond)
		}
		encoded, _ := encode(peerConnection.LocalDescription())
		fmt.Println("\n--- COPY THIS INITIAL SDP OFFER ---")
		fmt.Println(encoded)
		fmt.Println("--- END SDP OFFER ---")
		sdpStr := readMultilineInput("Paste initial SDP answer (base64):")
		answer, err := decode(sdpStr)
		must(err)
		must(peerConnection.SetRemoteDescription(*answer))
	} else {
		sdpStr := readMultilineInput("Paste initial SDP offer (base64):")
		offer, err := decode(sdpStr)
		must(err)
		must(peerConnection.SetRemoteDescription(*offer))
		answer, err := peerConnection.CreateAnswer(nil)
		must(err)
		must(peerConnection.SetLocalDescription(answer))
		for peerConnection.ICEGatheringState() != webrtc.ICEGatheringStateComplete {
			time.Sleep(100 * time.Millisecond)
		}
		encoded, _ := encode(peerConnection.LocalDescription())
		fmt.Println("\n--- COPY THIS INITIAL SDP ANSWER ---")
		fmt.Println(encoded)
		fmt.Println("--- END SDP ANSWER ---")
	}

	// Prompt if the user wants to share audio/video
	var shareAV string
	fmt.Print("Do you want to share audio/video? (yes/no): ")
	fmt.Scanln(&shareAV)
	if strings.ToLower(strings.TrimSpace(shareAV)) == "yes" {
		// Close the initial connection and start a new one for media negotiation.
		peerConnection.Close()
		peerConnection, err = webrtc.NewPeerConnection(webrtc.Configuration{
			ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
		})
		must(err)
		if *role == "offer" {
			// Create and add AV tracks for new connection
			videoTrack, err := webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
				"video", "pion",
			)
			must(err)
			audioTrack, err := webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
				"audio", "pion",
			)
			must(err)
			_, err = peerConnection.AddTrack(videoTrack)
			must(err)
			_, err = peerConnection.AddTrack(audioTrack)
			must(err)
			startFFmpegToPipe(videoTrack, "video")
			startFFmpegToPipe(audioTrack, "audio")
			// Fresh offer for AV sharing
			offer, err := peerConnection.CreateOffer(nil)
			must(err)
			must(peerConnection.SetLocalDescription(offer))
			for peerConnection.ICEGatheringState() != webrtc.ICEGatheringStateComplete {
				time.Sleep(100 * time.Millisecond)
			}
			encoded, _ := encode(peerConnection.LocalDescription())
			fmt.Println("\n--- COPY THIS AV SDP OFFER ---")
			fmt.Println(encoded)
			fmt.Println("--- END AV SDP OFFER ---")
			sdpStr := readMultilineInput("Paste AV SDP answer (base64):")
			answer, err := decode(sdpStr)
			must(err)
			must(peerConnection.SetRemoteDescription(*answer))
		} else { // answer role
			// Wait for AV offer then add AV tracks
			sdpStr := readMultilineInput("Paste AV SDP offer (base64):")
			offer, err := decode(sdpStr)
			must(err)
			must(peerConnection.SetRemoteDescription(*offer))
			videoTrack, err := webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
				"video", "pion",
			)
			must(err)
			audioTrack, err := webrtc.NewTrackLocalStaticSample(
				webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus},
				"audio", "pion",
			)
			must(err)
			_, err = peerConnection.AddTrack(videoTrack)
			must(err)
			_, err = peerConnection.AddTrack(audioTrack)
			must(err)
			startFFmpegToPipe(videoTrack, "video")
			startFFmpegToPipe(audioTrack, "audio")
			answer, err := peerConnection.CreateAnswer(nil)
			must(err)
			must(peerConnection.SetLocalDescription(answer))
			for peerConnection.ICEGatheringState() != webrtc.ICEGatheringStateComplete {
				time.Sleep(100 * time.Millisecond)
			}
			encoded, _ := encode(peerConnection.LocalDescription())
			fmt.Println("\n--- COPY THIS AV SDP ANSWER ---")
			fmt.Println(encoded)
			fmt.Println("--- END AV SDP ANSWER ---")
		}
	}

	fmt.Println("Connection established. Streaming AV (if enabled). Press Ctrl+C to exit.")

	// Setup signal channel for termination
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	<-signalChan

	fmt.Println("Shutting down...")
	for _, cmd := range ffmpegProcs {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	os.Exit(0)
}
