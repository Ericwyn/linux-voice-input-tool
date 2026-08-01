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
	sampleRate  int
	stream      *portaudio.Stream
	buffer      []int16
	mu          sync.Mutex
	recording   bool
	AudioStream chan []float32
}

func NewRecorder(sampleRate int) *Recorder {
	return &Recorder{
		sampleRate:  sampleRate,
		AudioStream: make(chan []float32, 64),
	}
}

func Init() error {
	return portaudio.Initialize()
}

func Terminate() {
	portaudio.Terminate()
}

func int16ToFloat32(samples []int16) []float32 {
	result := make([]float32, len(samples))
	for i, sample := range samples {
		result[i] = float32(sample) / 32768.0
	}
	return result
}

func (r *Recorder) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.recording {
		return fmt.Errorf("already recording")
	}

	r.buffer = make([]int16, 0, r.sampleRate*60)

	frameSize := 1024

	stream, err := portaudio.OpenDefaultStream(1, 0, float64(r.sampleRate), frameSize, func(in []int16) {
		chunk := make([]int16, len(in))
		copy(chunk, in)

		r.mu.Lock()
		if r.recording {
			r.buffer = append(r.buffer, chunk...)
		}
		r.mu.Unlock()

		select {
		case r.AudioStream <- int16ToFloat32(chunk):
		default:
		}
	})
	if err != nil {
		return fmt.Errorf("open audio stream: %w", err)
	}

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

	if stream != nil {
		if err := stream.Stop(); err != nil {
			return err
		}
		if err := stream.Close(); err != nil {
			return err
		}
	}

	close(r.AudioStream)
	return nil
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

	f.Write([]byte("RIFF"))
	binary.Write(f, binary.LittleEndian, int32(fileSize))
	f.Write([]byte("WAVE"))

	f.Write([]byte("fmt "))
	binary.Write(f, binary.LittleEndian, int32(16))
	binary.Write(f, binary.LittleEndian, int16(1))
	binary.Write(f, binary.LittleEndian, int16(1))
	binary.Write(f, binary.LittleEndian, int32(r.sampleRate))
	binary.Write(f, binary.LittleEndian, int32(r.sampleRate*2))
	binary.Write(f, binary.LittleEndian, int16(2))
	binary.Write(f, binary.LittleEndian, int16(16))

	f.Write([]byte("data"))
	binary.Write(f, binary.LittleEndian, int32(dataSize))

	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, data)
	f.Write(buf.Bytes())

	return nil
}
