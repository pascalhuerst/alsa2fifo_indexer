package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bogem/id3v2"
	"github.com/fsnotify/fsnotify"
)

const (
	fadeTime = 0.8
)

type fileServer struct {
	chunkDirPath      string
	sessionDirPath    string
	recordingsDirPath string
	lock              sync.Locker
	Recorders         map[string]Recorder
	sessionTTL        time.Duration
	renderRequestCH   chan RenderRequest
}

// Recorder holds open sessions for a recorder
type Recorder struct {
	OpenSessions []OpenSession `json:"open_sessions,omitempty"`
}

// OpenSession represents an open session which can be closes to a recording with this server
type OpenSession struct {
	ID               string    `json:"id,omitempty"`
	WAVFileName      string    `json:"wav_file_name,omitempty"`
	OGGFileName      string    `json:"ogg_file_name,omitempty"`
	WaveformFileName string    `json:"waveform_file_name,omitempty"`
	Timestamp        time.Time `json:"timestamp,omitempty"`
	HoursToLive      float64   `json:"hours_to_live,omitempty"`
}

// A Segment is a cut mark
type Segment struct {
	Name      string   `json:"labelText,omitempty"`
	StartTime float32  `json:"startTime,omitempty"`
	EndTime   float32  `json:"endTime,omitempty"`
	Filetypes []string `json:"filetypes,omitempty"`
}

// RenderRequest is issues by frontend to session -> recording
type RenderRequest struct {
	Segments   map[string]Segment `json:"segments,omitempty"`
	RecorderID string             `json:"recorderID,omitempty"`
	SessionID  string             `json:"sessionID,omitempty"`
}

func main() {

	chunkDir := flag.String("chunk", "chunks", "Directory to look for chunks")
	sessionDir := flag.String("session", "sessions", "Directory to look for sessions")
	recordingsDirPath := flag.String("recordings", "recordings", "Directory to store recordings")
	sessionTTL := flag.Duration("age", time.Duration(3*24*time.Hour), "Duration to keep sessions, before they are deleted")
	flag.Parse()

	fs := fileServer{
		chunkDirPath:      *chunkDir,
		sessionDirPath:    *sessionDir,
		recordingsDirPath: *recordingsDirPath,
		lock:              &sync.Mutex{},
		sessionTTL:        *sessionTTL,
		renderRequestCH:   make(chan RenderRequest),
	}

	fs.parseOpenSessions()

	server := http.FileServer(http.Dir(fs.sessionDirPath))
	http.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Access-Control-Allow-Origin", "*")
		server.ServeHTTP(rw, r)
	})

	http.HandleFunc("/introspect", fs.introspect)
	http.HandleFunc("/render", fs.render)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					fs.parseOpenSessions()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				fmt.Println("error:", err)
			case <-time.After(time.Minute * 5):
				fmt.Println("Checking sessions directory")
				fs.parseOpenSessions()
				fmt.Println("Checking sessions directory - Done.")
			case request := <-fs.renderRequestCH:
				fmt.Printf("RenderRequest: %v\n", request)
				fs.renderRequest(request)
			}
		}
	}()

	err = watcher.Add(fs.sessionDirPath)
	if err != nil {
		log.Fatal(err)
	}

	http.ListenAndServe(":8234", nil)
}

func (f fileServer) renderRequest(r RenderRequest) error {

	sourceFilePathRel := filepath.Join(f.sessionDirPath, r.RecorderID, r.SessionID, "data.wav")
	sourceFilePath, err := filepath.Abs(sourceFilePathRel)
	if err != nil {
		fmt.Printf("Cannot get absolute path: %v\n", err)
		return fmt.Errorf("Cannot get absolute path: %v", err)
	}

	createAudioFile := func(name, fileExtension string, startTime, endTime float32) {
		targetAudioFilePathRel := filepath.Join(f.recordingsDirPath, fmt.Sprintf("domestic_affairs_%s.%s", name, fileExtension))
		targetAudioFilePath, err := filepath.Abs(targetAudioFilePathRel)
		if err != nil {
			fmt.Printf("Cannot get absolute path: %v\n", err)
			return
		}

		strFadeTime := fmt.Sprintf("%.1f", fadeTime)

		soxCmd := exec.Command("/usr/bin/sox", sourceFilePath, targetAudioFilePath, "trim", fmt.Sprintf("%v", startTime), fmt.Sprintf("=%v", endTime), "fade", strFadeTime, "-0", strFadeTime, "norm", "-0.1")
		fmt.Printf("Create: %s\n", targetAudioFilePathRel)

		err = soxCmd.Start()
		if err != nil {
			fmt.Printf("Cannot create %s file: %v\n", fileExtension, err)
			return
		}

		err = soxCmd.Wait()
		if err != nil {
			fmt.Printf("Cannot create %s file: %v\n", fileExtension, err)
			return
		}
		fmt.Printf("Create: %s - Done.\n", targetAudioFilePathRel)
		fmt.Printf("Write ID3 Tag: %s\n", targetAudioFilePathRel)

		tag, err := id3v2.Open(targetAudioFilePath, id3v2.Options{Parse: true})
		if err != nil {
			fmt.Printf("Cannot write ID3 Tag: %v\n", err)
			return
		}
		defer tag.Close()

		tag.SetArtist("Paso")
		tag.SetTitle("DA#13")
		tag.SetYear(fmt.Sprintf("%d", time.Now().Year()))
		tag.SetAlbum("Domestic Affairs Recordings")

		artwork, err := ioutil.ReadFile("logo_black.png")
		if err != nil {
			fmt.Printf("Cannot read artwork: %v\n", err)
		}

		pic := id3v2.PictureFrame{
			Encoding:    id3v2.EncodingUTF8,
			MimeType:    "image/png",
			PictureType: id3v2.PTFrontCover,
			Description: "Front cover",
			Picture:     artwork,
		}
		tag.AddAttachedPicture(pic)
		// Write tag to file.
		if err = tag.Save(); err != nil {
			fmt.Printf("Cannot write ID3 Tag: %v\n", err)
			return
		}

		fmt.Printf("Write ID3 Tag: %s - Done.\n", targetAudioFilePathRel)
	}

	for _, value := range r.Segments {
		for _, filetype := range value.Filetypes {
			fixedName := strings.ReplaceAll(value.Name, " ", "_")

			go createAudioFile(fixedName, filetype, value.StartTime, value.EndTime)
		}
	}

	return nil
}

func (f *fileServer) parseOpenSessions() error {

	ret := make(map[string]Recorder, 1)

	recorders, err := ioutil.ReadDir(f.sessionDirPath)
	if err != nil {
		return fmt.Errorf("Cannot read recorders in: %v", f.sessionDirPath)
	}

	newSessions := []OpenSession{}

	for _, recorder := range recorders {
		sessionsPath := filepath.Join(f.sessionDirPath, recorder.Name())
		ss, err := ioutil.ReadDir(sessionsPath)
		if err != nil {
			return fmt.Errorf("Cannot read sessions in: %v", sessionsPath)
		}

		for _, s := range ss {
			epoche, err := strconv.ParseInt(s.Name(), 10, 64)
			if err != nil {
				return fmt.Errorf("Cannot parse epoche: %s", s.Name())
			}

			toLive := f.sessionTTL - time.Duration(time.Now().Sub(time.Unix(0, epoche)))
			fmt.Printf("Session [%s] has %f hours left, before it gets deleted\n", s.Name(), toLive.Hours())

			if toLive.Hours() < 0 {
				toDelete := filepath.Join(f.sessionDirPath, recorder.Name(), s.Name())
				fmt.Printf("Attempting to delete: %s\n", toDelete)
				err = os.RemoveAll(toDelete)
				if err != nil {
					fmt.Printf("Cannot remove folder: %v\n", err)
				}
				continue
			}

			session := OpenSession{
				ID:               s.Name(),
				OGGFileName:      "data.ogg",
				WAVFileName:      "data.wav",
				WaveformFileName: "waveform.dat",
				Timestamp:        time.Unix(0, epoche),
				HoursToLive:      toLive.Hours(),
			}
			newSessions = append(newSessions, session)
		}
		ret[recorder.Name()] = Recorder{
			OpenSessions: newSessions,
		}
	}

	f.lock.Lock()
	f.Recorders = ret
	f.lock.Unlock()

	return nil
}

func (f fileServer) introspect(w http.ResponseWriter, r *http.Request) {

	f.lock.Lock()
	defer f.lock.Unlock()

	js, err := json.Marshal(f.Recorders)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

func (f fileServer) render(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	renderRequest := RenderRequest{}

	if r.Method == "POST" {

		err := json.NewDecoder(r.Body).Decode(&renderRequest)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.renderRequestCH <- renderRequest
	}

	w.Write([]byte("Success"))

}
