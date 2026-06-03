package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"sync"

	"github.com/gordonklaus/portaudio"
)

type Recorder struct {
	sampleRate int
	stream     *portaudio.Stream
	buffer     []int16
	mu         sync.Mutex
	recording  bool
}

func NewRecorder(sampleRate int) *Recorder {
	return &Recorder{
		sampleRate: sampleRate,
	}
}

func Init() error {
	return portaudio.Initialize()
}

func Terminate() {
	portaudio.Terminate()
}

func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return fmt.Errorf("already recording")
	}

	r.buffer = make([]int16, 0, r.sampleRate*60)

	frameSize := 1024
	inputBuffer := make([]int16, frameSize)

	stream, err := portaudio.OpenDefaultStream(1, 0, float64(r.sampleRate), frameSize, func(in []int16) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.recording {
			r.buffer = append(r.buffer, in...)
		}
	})
	if err != nil {
		return fmt.Errorf("open audio stream: %w", err)
	}
	_ = inputBuffer

	r.stream = stream
	r.recording = true
	return stream.Start()
}

func (r *Recorder) Stop() error {
	r.mu.Lock()
	if !r.recording {
		r.mu.Unlock()
		return fmt.Errorf("not recording")
	}

	stream := r.stream
	r.recording = false
	r.stream = nil
	r.mu.Unlock()

	if stream == nil {
		return nil
	}
	if err := stream.Stop(); err != nil {
		return err
	}
	return stream.Close()
}

func (r *Recorder) SaveWAV(filepath string) error {
	r.mu.Lock()
	data := make([]int16, len(r.buffer))
	copy(data, r.buffer)
	r.mu.Unlock()

	if len(data) == 0 {
		return fmt.Errorf("no audio data recorded")
	}

	f, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer f.Close()

	numSamples := len(data)
	dataSize := numSamples * 2
	fileSize := 36 + dataSize

	// WAV header
	f.Write([]byte("RIFF"))
	binary.Write(f, binary.LittleEndian, int32(fileSize))
	f.Write([]byte("WAVE"))

	// fmt chunk
	f.Write([]byte("fmt "))
	binary.Write(f, binary.LittleEndian, int32(16))        // chunk size
	binary.Write(f, binary.LittleEndian, int16(1))         // PCM format
	binary.Write(f, binary.LittleEndian, int16(1))         // mono
	binary.Write(f, binary.LittleEndian, int32(r.sampleRate))
	binary.Write(f, binary.LittleEndian, int32(r.sampleRate*2)) // byte rate
	binary.Write(f, binary.LittleEndian, int16(2))         // block align
	binary.Write(f, binary.LittleEndian, int16(16))        // bits per sample

	// data chunk
	f.Write([]byte("data"))
	binary.Write(f, binary.LittleEndian, int32(dataSize))

	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, data)
	f.Write(buf.Bytes())

	return nil
}
