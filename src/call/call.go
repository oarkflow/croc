package call

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pion/mediadevices" // Register camera driver
	// Register microphone driver
	"github.com/pion/webrtc/v4"
	"github.com/schollz/croc/v10/src/croc"
	"github.com/schollz/croc/v10/src/message"
	"github.com/schollz/croc/v10/src/tcp"
	log "github.com/schollz/logger"
)

// signalSDP exchanges SDP between peers using signaling over the TCP relay.
func signalSDP(pc *webrtc.PeerConnection, relayAddr, relayPass, roomName string) error {
	// Connect to the relay server for signaling.
	conn, _, _, err := tcp.ConnectToTCPServer(relayAddr, relayPass, roomName, 30*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Create and set the local offer.
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err = pc.SetLocalDescription(offer); err != nil {
		return err
	}
	offerData, err := json.Marshal(offer)
	if err != nil {
		return err
	}
	sigMsg := message.Message{
		Type:    "webrtc_offer",
		Message: string(offerData),
	}
	data, err := json.Marshal(sigMsg)
	if err != nil {
		return err
	}
	if err = conn.Send(data); err != nil {
		return err
	}

	// Wait and read SDP answer.
	answerData, err := conn.Receive()
	if err != nil {
		return err
	}
	// Debug log raw answerData in case of error.
	log.Debugf("Received SDP answer: %s", string(answerData))
	var ansMsg message.Message
	if err = json.Unmarshal(answerData, &ansMsg); err != nil {
		return fmt.Errorf("failed to unmarshal SDP answer: %v\nraw data: %s", err, string(answerData))
	}
	if ansMsg.Type != "webrtc_answer" {
		return fmt.Errorf("unexpected signaling type: %s", ansMsg.Type)
	}
	var answer webrtc.SessionDescription
	if err = json.Unmarshal([]byte(ansMsg.Message), &answer); err != nil {
		return fmt.Errorf("failed to unmarshal remote SDP: %v\nraw SDP: %s", err, ansMsg.Message)
	}
	return pc.SetRemoteDescription(answer)
}

// StartAudioCall establishes a robust, real-time audio streaming session using WebRTC and actual microphone capture.
func StartAudioCall(options croc.Options) error {
	// Create MediaEngine and register default codecs.
	m := webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return err
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))
	// Configure PeerConnection.
	config := webrtc.Configuration{
		ICETransportPolicy: webrtc.ICETransportPolicyAll,
	}
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return err
	}

	// Before capturing, enumerate devices.
	audioDevices := mediadevices.EnumerateDevices()
	hasAudio := false
	for _, d := range audioDevices {
		if d.Kind == mediadevices.AudioInput {
			hasAudio = true
			break
		}
	}
	if !hasAudio {
		return fmt.Errorf("no microphone detected on this machine")
	}
	// Capture real audio using mediadevices.
	stream, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {
			// Default constraints; you can refine and select device ID as needed.
		},
		Video: nil,
	})
	if err != nil {
		return fmt.Errorf("failed to capture audio: %v", err)
	}
	// Add all captured audio tracks to the PeerConnection.
	for _, track := range stream.GetAudioTracks() {
		if _, err = pc.AddTrack(track); err != nil {
			return fmt.Errorf("failed to add audio track: %v", err)
		}
	}

	// Wait for ICE connection.
	connectedChan := make(chan struct{})
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Debugf("ICE connection state: %s", state.String())
		if state == webrtc.ICEConnectionStateConnected {
			close(connectedChan)
		}
	})
	// Exchange SDP via relay.
	if err = signalSDP(pc, options.RelayAddress, options.RelayPassword, options.RoomName); err != nil {
		return err
	}
	log.Debug("SDP exchange complete, waiting for peer connection...")
	select {
	case <-connectedChan:
		log.Debug("Peer connected!")
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for ICE connection")
	}
	log.Debug("Starting real-time audio streaming...")

	// Block until user ends the call.
	fmt.Println("Audio call established. Press Enter to end call.")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
	pc.Close()
	fmt.Println("Audio call ended.")
	return nil
}

// StartVideoCall establishes a robust, real-time video streaming session using WebRTC and actual camera capture.
func StartVideoCall(options croc.Options) error {
	m := webrtc.MediaEngine{}
	if err := m.RegisterDefaultCodecs(); err != nil {
		return err
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))
	config := webrtc.Configuration{
		ICETransportPolicy: webrtc.ICETransportPolicyAll,
	}
	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return err
	}

	// Before capturing video, enumerate devices.
	videoDevices := mediadevices.EnumerateDevices()
	hasVideo := false
	for _, d := range videoDevices {
		if d.Kind == mediadevices.VideoInput {
			hasVideo = true
			break
		}
	}
	if !hasVideo {
		return fmt.Errorf("no webcam detected on this machine")
	}
	// Capture real video using mediadevices.
	stream, err := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Video: func(c *mediadevices.MediaTrackConstraints) {
			// Default video constraints; customize camera resolution, etc., if needed.
		},
		Audio: nil,
	})
	if err != nil {
		return fmt.Errorf("failed to capture video: %v", err)
	}
	// Add all captured video tracks to the PeerConnection.
	for _, track := range stream.GetVideoTracks() {
		if _, err = pc.AddTrack(track); err != nil {
			return fmt.Errorf("failed to add video track: %v", err)
		}
	}

	connectedChan := make(chan struct{})
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Debugf("ICE connection state: %s", state.String())
		if state == webrtc.ICEConnectionStateConnected {
			close(connectedChan)
		}
	})
	if err = signalSDP(pc, options.RelayAddress, options.RelayPassword, options.RoomName); err != nil {
		return err
	}
	log.Debug("SDP exchange complete, waiting for peer connection...")
	select {
	case <-connectedChan:
		log.Debug("Peer connected!")
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for ICE connection")
	}
	log.Debug("Starting real-time video streaming...")

	// Block until user ends the call.
	fmt.Println("Video call established. Press Enter to end call.")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
	pc.Close()
	fmt.Println("Video call ended.")
	return nil
}
