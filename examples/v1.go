package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gordonklaus/portaudio"
	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/codec/opus"
	"github.com/pion/mediadevices/pkg/codec/x264"
	_ "github.com/pion/mediadevices/pkg/driver/camera"
	_ "github.com/pion/mediadevices/pkg/driver/microphone"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/webrtc/v4"
)

func main() {
	role := flag.String("role", "offer", "Role: offer or answer")
	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		log.Fatal(err)
	}

	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind() == webrtc.RTPCodecTypeAudio {
			log.Println("Received audio track, playing...")
			go handleAudioTrack(track)
		} else {
			log.Println("Received track of kind:", track.Kind())
		}
	})
	// Create a new RTCPeerConnection
	x264Params, err := x264.NewParams()
	if err != nil {
		panic(err)
	}
	x264Params.BitRate = 500_000 // 500kbps

	opusParams, err := opus.NewParams()
	if err != nil {
		panic(err)
	}
	codecSelector := mediadevices.NewCodecSelector(
		mediadevices.WithVideoEncoders(&x264Params),
		mediadevices.WithAudioEncoders(&opusParams),
	)
	videoStream, _ := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Video: func(c *mediadevices.MediaTrackConstraints) {
			c.Width = prop.Int(600)
			c.Height = prop.Int(400)
		},
		Audio: func(c *mediadevices.MediaTrackConstraints) {
			// removed codec assignment
		},
		Codec: codecSelector,
	}) // added audio encoder option
	audioStream, _ := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {
			// removed codec assignment
		},
		Codec: codecSelector,
	})
	if videoStream != nil {
		for _, track := range videoStream.GetTracks() {
			_, err = peerConnection.AddTrack(track)
			if err != nil {
				log.Println("Error adding track:", err)
			}
		}
		log.Println("Peer connection created. Waiting for signaling...")
	}
	if videoStream == nil && audioStream != nil {
		for _, track := range audioStream.GetTracks() {
			_, err = peerConnection.AddTrack(track)
			if err != nil {
				log.Println("Error adding track:", err)
			}
		}
		log.Println("Peer connection created. Waiting for signaling...")
	}

	// --- Offer/Answer signaling using multi-line SDP ---
	if *role == "offer" {
		offer, err := peerConnection.CreateOffer(nil)
		if err != nil {
			log.Fatalf("Failed to create offer: %v", err)
		}
		if err = peerConnection.SetLocalDescription(offer); err != nil {
			log.Fatalf("Failed to set local description: %v", err)
		}
		fmt.Println("Copy the following offer SDP to the remote peer:")
		fmt.Println(offer.SDP)
		fmt.Println("Paste remote answer SDP (end with empty line):")
		answerSDP := readSDP()
		answer := webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answerSDP}
		if err = peerConnection.SetRemoteDescription(answer); err != nil {
			log.Fatalf("SetRemoteDescription: %v", err)
		}
	} else if *role == "answer" {
		fmt.Println("Paste remote offer SDP (end with empty line):")
		offerSDP := readSDP()
		offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}
		if err = peerConnection.SetRemoteDescription(offer); err != nil {
			log.Fatalf("SetRemoteDescription: %v", err)
		}
		answer, err := peerConnection.CreateAnswer(nil)
		if err != nil {
			log.Fatalf("Failed to create answer: %v", err)
		}
		if err = peerConnection.SetLocalDescription(answer); err != nil {
			log.Fatalf("Failed to set local description: %v", err)
		}
		// Wait for ICE gathering to complete so LocalDescription includes ice-ufrag.
		for peerConnection.ICEGatheringState() != webrtc.ICEGatheringStateComplete {
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Println("Copy the following answer SDP to the remote peer:")
		fmt.Println(peerConnection.LocalDescription().SDP)
	}
	// --- End signaling ---

	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Hour):
		return
	}
}

// readSDP reads multi-line SDP from stdin until an empty line is encountered.
func readSDP() string {
	reader := bufio.NewReader(os.Stdin)
	var sdpLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		sdpLines = append(sdpLines, line)
	}
	return strings.Join(sdpLines, "")
}

// handleAudioTrack receives audio RTP packets and writes the decoded samples to the default audio output.
func handleAudioTrack(track *webrtc.TrackRemote) {
	// Initialize PortAudio for audio playback.
	if err := portaudio.Initialize(); err != nil {
		log.Println("PortAudio initialize error:", err)
		return
	}
	defer portaudio.Terminate()

	// Buffer size: 960 samples (adjust as needed for your sample rate/latency)
	outBuffer := make([]int16, 960)
	stream, err := portaudio.OpenDefaultStream(0, 1, 48000, len(outBuffer), &outBuffer)
	if err != nil {
		log.Println("Error opening PortAudio stream:", err)
		return
	}
	if err = stream.Start(); err != nil {
		log.Println("Error starting PortAudio stream:", err)
		return
	}
	defer stream.Stop()

	for {
		rtpPacket, _, err := track.ReadRTP()
		if err != nil {
			log.Println("Error reading RTP packet:", err)
			break
		}
		payload := rtpPacket.Payload
		// Convert payload bytes to []int16 (assuming little-endian PCM)
		samplesCount := len(payload) / 2
		samples := make([]int16, samplesCount)
		for i := 0; i < samplesCount; i++ {
			samples[i] = int16(binary.LittleEndian.Uint16(payload[i*2 : i*2+2]))
		}
		// Copy samples into our fixed-size outBuffer.
		if samplesCount < len(outBuffer) {
			copy(outBuffer, samples)
			for j := samplesCount; j < len(outBuffer); j++ {
				outBuffer[j] = 0
			}
		} else {
			copy(outBuffer, samples[:len(outBuffer)])
		}
		if err = stream.Write(); err != nil {
			log.Println("Error writing to PortAudio stream:", err)
			break
		}
	}
}
