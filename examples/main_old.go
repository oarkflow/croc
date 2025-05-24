package main

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"os"
	"time"

	"github.com/pion/mediadevices"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/mediadevices/pkg/wave"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media" // ...existing import...

	// This is required to register camera and microphone adapters
	_ "github.com/pion/mediadevices/pkg/driver/camera"
	_ "github.com/pion/mediadevices/pkg/driver/microphone"
	// _ "github.com/pion/mediadevices/pkg/driver/videotest"
)

func mai1n() {
	// Create a new WebRTC PeerConnection
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		panic(err)
	}

	// Obtain media streams
	videoStream, vidErr := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Video: func(c *mediadevices.MediaTrackConstraints) {
			c.Width = prop.Int(600)
			c.Height = prop.Int(400)
		},
	})
	audioStream, audErr := mediadevices.GetUserMedia(mediadevices.MediaStreamConstraints{
		Audio: func(c *mediadevices.MediaTrackConstraints) {
		},
	})

	// Only stream video if a video device exists
	if videoStream != nil && len(videoStream.GetVideoTracks()) > 0 && vidErr == nil {
		videoTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
		if err != nil {
			panic(err)
		}
		if _, err = pc.AddTrack(videoTrack); err != nil {
			panic(err)
		}
		go streamVideo(videoStream, videoTrack)
	}

	// Only stream audio if an audio device exists
	if audioStream != nil && len(audioStream.GetAudioTracks()) > 0 && audErr == nil {
		audioTrack, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
		if err != nil {
			panic(err)
		}
		if _, err = pc.AddTrack(audioTrack); err != nil {
			panic(err)
		}
		fmt.Println("Streaming audio...")
		go streamAudio(audioStream, audioTrack)
	}

	// Block forever (or implement proper shutdown logic)
	select {}
}

func streamVi1deo(stream mediadevices.MediaStream, videoTrack *webrtc.TrackLocalStaticSample) {
	// Use the first available video track
	track := stream.GetVideoTracks()[0]
	vt := track.(*mediadevices.VideoTrack)
	videoReader := vt.NewReader(false)
	defer vt.Close()

	for {
		frame, release, err := videoReader.Read()
		if err != nil {
			// ...error handling...
			continue
		}
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, frame, nil); err != nil {
			release()
			continue
		}
		sample := media.Sample{Data: buf.Bytes(), Duration: time.Second / 30} // changed type here
		if err := videoTrack.WriteSample(sample); err != nil {
			// ...error handling...
		}
		release()
	}
}

func streamAu1dio(stream mediadevices.MediaStream, audioTrack *webrtc.TrackLocalStaticSample) {
	track := stream.GetAudioTracks()[0]
	at := track.(*mediadevices.AudioTrack)
	audioReader := at.NewReader(false)
	defer at.Close()

	// Open WAV file for writing once
	wavFile, err := os.Create("output.wav")
	if err != nil {
		fmt.Println("Error creating wav file:", err)
	}
	defer wavFile.Close()
	var totalBytesWritten uint32
	headerWritten := false

	for {
		audioData, release, err := audioReader.Read()
		if err != nil {
			fmt.Println("Error reading audio data:", err)
			continue
		}
		// Debug: log the type of received audioData and its length if possible
		fmt.Printf("Received audio data type: %T\n", audioData)
		var pcm []byte
		switch data := audioData.(type) {
		case wave.Audio:
			info := data.ChunkInfo()
			fmt.Printf("wave.Audio: Len=%d, Channels=%d, SamplingRate=%d\n", info.Len, info.Channels, info.SamplingRate)
			pcm = make([]byte, info.Len*info.Channels*2)
			for i := 0; i < info.Len; i++ {
				for ch := 0; ch < info.Channels; ch++ {
					s := data.At(i, ch)
					conv := wave.Int16SampleFormat.Convert(s)
					sampleInt := int16(conv.Int())
					idx := (i*info.Channels + ch) * 2
					pcm[idx] = byte(sampleInt)
					pcm[idx+1] = byte(sampleInt >> 8)
				}
			}
		case *wave.Int16Interleaved:
			if data.Data == nil || len(data.Data) == 0 {
				fmt.Println("Received *wave.Int16Interleaved with empty data")
				release()
				continue
			}
			fmt.Printf("*wave.Int16Interleaved: length=%d\n", len(data.Data))
			pcm = make([]byte, len(data.Data)*2)
			for i, sample := range data.Data {
				idx := i * 2
				pcm[idx] = byte(sample)
				pcm[idx+1] = byte(sample >> 8)
			}
		default:
			fmt.Println("Unsupported audio data type:", fmt.Sprintf("%T", data))
			release()
			continue
		}

		fmt.Printf("PCM chunk size: %d bytes\n", len(pcm))
		// Write WAV header once using info from the first valid chunk (if not already done)
		if !headerWritten && wavFile != nil {
			var numChannels uint16 = 1
			var sampleRate uint32 = 48000
			if aud, ok := audioData.(wave.Audio); ok {
				info := aud.ChunkInfo()
				numChannels = uint16(info.Channels)
				sampleRate = uint32(info.SamplingRate)
			} else if inter, ok := audioData.(*wave.Int16Interleaved); ok && inter.ChunkInfo != nil {
				info := inter.ChunkInfo()
				numChannels = uint16(info.Channels)
				sampleRate = uint32(info.SamplingRate)
			}
			bitsPerSample := uint16(16)
			byteRate := sampleRate * uint32(numChannels) * uint32(bitsPerSample) / 8
			blockAlign := numChannels * bitsPerSample / 8

			var header [44]byte
			copy(header[0:4], []byte("RIFF"))
			// file size (4 bytes) will be updated later
			copy(header[8:12], []byte("WAVE"))
			copy(header[12:16], []byte("fmt "))
			header[16] = 16                // Subchunk1Size for PCM
			header[20] = 1                 // AudioFormat PCM = 1
			header[22] = byte(numChannels) // NumChannels
			header[23] = byte(numChannels >> 8)
			header[24] = byte(sampleRate) // SampleRate
			header[25] = byte(sampleRate >> 8)
			header[26] = byte(sampleRate >> 16)
			header[27] = byte(sampleRate >> 24)
			header[28] = byte(byteRate) // ByteRate
			header[29] = byte(byteRate >> 8)
			header[30] = byte(byteRate >> 16)
			header[31] = byte(byteRate >> 24)
			header[32] = byte(blockAlign) // BlockAlign
			header[33] = byte(blockAlign >> 8)
			header[34] = byte(bitsPerSample) // BitsPerSample
			header[35] = 0
			copy(header[36:40], []byte("data"))
			// Subchunk2Size (data size), to be updated later
			if _, err := wavFile.Write(header[:]); err != nil {
				fmt.Println("Error writing WAV header:", err)
			} else {
				fmt.Println("WAV header written.")
			}
			headerWritten = true
		}
		// Write PCM data to file if wavFile is open
		if wavFile != nil {
			n, err := wavFile.Write(pcm)
			if err != nil {
				fmt.Println("Error writing PCM data to wav file:", err)
			} else {
				fmt.Printf("Wrote %d bytes to wav file.\n", n)
			}
			totalBytesWritten += uint32(n)
			// Update header:
			currentPos, _ := wavFile.Seek(0, 1)
			fileSize := 36 + totalBytesWritten
			wavFile.Seek(4, 0)
			wavFile.Write([]byte{
				byte(fileSize),
				byte(fileSize >> 8),
				byte(fileSize >> 16),
				byte(fileSize >> 24),
			})
			wavFile.Seek(40, 0)
			wavFile.Write([]byte{
				byte(totalBytesWritten),
				byte(totalBytesWritten >> 8),
				byte(totalBytesWritten >> 16),
				byte(totalBytesWritten >> 24),
			})
			wavFile.Seek(currentPos, 0)
			// Force flush the file to disk.
			wavFile.Sync()
		}
		sample := media.Sample{Data: pcm, Duration: time.Millisecond * 20} // approximate duration
		if err := audioTrack.WriteSample(sample); err != nil {
			fmt.Println("Error writing sample to audioTrack:", err)
		}
		release()
	}
}

func streamAudioO1ldKeep(stream mediadevices.MediaStream, audioTrack *webrtc.TrackLocalStaticSample) {
	track := stream.GetAudioTracks()[0]
	at := track.(*mediadevices.AudioTrack)
	audioReader := at.NewReader(false)
	defer at.Close()
	for {
		audioData, release, err := audioReader.Read()
		if err != nil {
			fmt.Println("Error reading audio data:", err)
			continue
		}
		aud, ok := audioData.(wave.Audio)
		if !ok {
			fmt.Println("Received data is not of type wave.Audio")
			release()
			continue
		}
		info := aud.ChunkInfo()
		pcm := make([]byte, info.Len*info.Channels*2)
		for i := 0; i < info.Len; i++ {
			for ch := 0; ch < info.Channels; ch++ {
				sample := aud.At(i, ch)
				// Convert sample to int16 by shifting (assuming the sample is in 32-bit)
				sampleInt := int16(sample.Int() >> 16)
				idx := (i*info.Channels + ch) * 2
				pcm[idx] = byte(sampleInt)
				pcm[idx+1] = byte(sampleInt >> 8)
			}
		}
		sample := media.Sample{Data: pcm, Duration: time.Millisecond * 20} // approximate duration
		if err := audioTrack.WriteSample(sample); err != nil {
			// ...error handling...
		}
		release()
	}
}
