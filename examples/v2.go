package main

import (
	"context"
	"log"
	"time"

	"github.com/pion/mediadevices"
	_ "github.com/pion/mediadevices/pkg/driver/camera"
	_ "github.com/pion/mediadevices/pkg/driver/microphone"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/webrtc/v4"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}},
	})
	if err != nil {
		log.Fatal(err)
	}
	videoStream, _ := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Video: func(c *mediadevices.MediaTrackConstraints) {
			c.Width = prop.Int(600)
			c.Height = prop.Int(400)
		},
		Audio: func(c *mediadevices.MediaTrackConstraints) {},
	})
	audioStream, _ := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {},
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
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Hour):
		return
	}
}
