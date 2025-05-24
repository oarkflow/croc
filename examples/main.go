package main

import (
	"encoding/binary"
	"fmt"
	"image/jpeg"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bytes"

	"github.com/pion/mediadevices"
	_ "github.com/pion/mediadevices/pkg/driver/camera"
	_ "github.com/pion/mediadevices/pkg/driver/microphone"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/mediadevices/pkg/wave"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func main() {
	// Capture interrupt to finalize WAV
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Create PeerConnection
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatalln("PeerConnection error:", err)
	}

	// Get audio/video
	videoStream, _ := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Video: func(c *mediadevices.MediaTrackConstraints) {
			c.Width = prop.Int(600)
			c.Height = prop.Int(400)
		},
	})
	audioStream, _ := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {},
	})

	// Add video if available
	if videoStream != nil && len(videoStream.GetVideoTracks()) > 0 {
		vt, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion",
		)
		if err == nil {
			pc.AddTrack(vt)
			go streamVideo(videoStream, vt)
		}
	}

	// Add audio if available
	audioDone := make(chan struct{})
	if audioStream != nil && len(audioStream.GetAudioTracks()) > 0 {
		at, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion",
		)
		if err == nil {
			pc.AddTrack(at)
			go streamAudio(audioStream, at, audioDone)
		}
	}

	// Wait for Ctrl+C to finish
	<-sigCh
	fmt.Println("Stopping streams, finalizing WAV...")
	close(audioDone)
	// Give audio goroutine time to flush and update header
	time.Sleep(500 * time.Millisecond)
}

func streamVideo(s mediadevices.MediaStream, t *webrtc.TrackLocalStaticSample) {
	vt := s.GetVideoTracks()[0].(*mediadevices.VideoTrack)
	r := vt.NewReader(false)
	defer vt.Close()

	for {
		frame, release, err := r.Read()
		if err == nil {
			buf := new(bytes.Buffer)
			jpeg.Encode(buf, frame, nil)
			t.WriteSample(media.Sample{Data: buf.Bytes(), Duration: time.Second / 30})
		}
		release()
	}
}

func streamAudio(s mediadevices.MediaStream, t *webrtc.TrackLocalStaticSample, done chan struct{}) {
	at := s.GetAudioTracks()[0].(*mediadevices.AudioTrack)
	r := at.NewReader(true)
	defer at.Close()

	// Create WAV file with header placeholders
	f, err := os.Create("output.wav")
	if err != nil {
		log.Fatalln("WAV create error:", err)
	}
	defer f.Close()

	// Write 44-byte header stencil
	f.WriteString("RIFF")
	binary.Write(f, binary.LittleEndian, uint32(0))
	f.WriteString("WAVEfmt ")
	binary.Write(f, binary.LittleEndian, uint32(16))
	binary.Write(f, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(f, binary.LittleEndian, uint16(0)) // channels placeholder
	binary.Write(f, binary.LittleEndian, uint32(0)) // sampleRate placeholder
	binary.Write(f, binary.LittleEndian, uint32(0)) // byteRate placeholder
	binary.Write(f, binary.LittleEndian, uint16(0)) // blockAlign placeholder
	binary.Write(f, binary.LittleEndian, uint16(16))
	f.WriteString("data")
	binary.Write(f, binary.LittleEndian, uint32(0)) // data size

	var total uint32
	var sr uint32
	var ch uint16
	first := true

	for {
		select {
		case <-done:
			// exit loop
			// header already updated per-chunk
			return
		default:
			// continue reading
		}

		data, release, err := r.Read()
		if err != nil {
			release()
			continue
		}
		inter, ok := data.(*wave.Int16Interleaved)
		if !ok {
			release()
			continue
		}

		if first {
			ci := inter.ChunkInfo()
			sr = uint32(ci.SamplingRate)
			ch = uint16(ci.Channels)

			// fill header placeholders
			f.Seek(22, 0)
			binary.Write(f, binary.LittleEndian, ch)
			f.Seek(24, 0)
			binary.Write(f, binary.LittleEndian, sr)
			br := sr * uint32(ch) * 16 / 8
			f.Seek(28, 0)
			binary.Write(f, binary.LittleEndian, br)
			ba := ch * 16 / 8
			f.Seek(32, 0)
			binary.Write(f, binary.LittleEndian, ba)
			f.Seek(44, 0)
			first = false
		}

		// Write PCM data directly using binary.Write for the WAV file
		err = binary.Write(f, binary.LittleEndian, inter.Data)
		if err != nil {
			log.Println("Error writing PCM data via binary.Write:", err)
		}
		total += uint32(len(inter.Data) * 2)

		// Update header sizes
		f.Seek(4, 0)
		binary.Write(f, binary.LittleEndian, uint32(36+total))
		f.Seek(40, 0)
		binary.Write(f, binary.LittleEndian, total)
		f.Seek(44+int64(total), 0)

		// For forwarding to WebRTC, convert inter.Data to bytes using binary.Write
		buf := new(bytes.Buffer)
		if err := binary.Write(buf, binary.LittleEndian, inter.Data); err != nil {
			log.Println("Error converting PCM for WebRTC:", err)
		}
		sample := media.Sample{Data: buf.Bytes(), Duration: time.Millisecond * 20}
		t.WriteSample(sample)

		release()
	}
}
