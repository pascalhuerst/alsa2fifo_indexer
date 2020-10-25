package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

func main() {
	fmt.Println("Hello! Indexer!...")

	watchDir := flag.String("dir", "", "Directory to watch for changes")
	flag.Parse()

	if *watchDir == "" {
		log.Fatal("You need to specify a directory to watch")
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				//log.Println("event:", event)
				if event.Op&fsnotify.Write == fsnotify.Write {
					log.Println("modified file:", event.Name)

					tokens := strings.Split(event.Name, "_")
					tokens = strings.Split(tokens[len(tokens)-1], ".")
					epoche, err := strconv.ParseInt(tokens[0], 10, 64)
					if err != nil {
						log.Fatal(err)
					}

					fmt.Println(time.Unix(0, epoche))

					tokens = strings.Split(event.Name, "/")
					fileName := tokens[len(tokens)-1]

					Upload(event.Name, fileName)

					os.Remove(fileName)

				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Println("error:", err)
			}
		}
	}()

	err = watcher.Add(*watchDir)
	if err != nil {
		log.Fatal(err)
	}
	<-done
}

func Upload(path, fileName string) {

	file, err := os.Open(path)
	if err != nil {
		log.Fatalln(err)
	}
	defer file.Close()

	var requestBody bytes.Buffer
	multiPartWriter := multipart.NewWriter(&requestBody)

	fileWriter, err := multiPartWriter.CreateFormFile("raw_audio", fileName)
	if err != nil {
		log.Println(err)
	}

	_, err = io.Copy(fileWriter, file)
	if err != nil {
		log.Println(err)
	}

	multiPartWriter.Close()

	// By now our original request body should have been populated, so let's just use it with our custom request
	req, err := http.NewRequest("POST", "http://server.lan:8080/upload", &requestBody)
	if err != nil {
		log.Println(err)
	}
	req.Header.Set("Content-Type", multiPartWriter.FormDataContentType())

	// Do the request
	client := &http.Client{}
	response, err := client.Do(req)
	if err != nil {
		log.Println(err)
		return
	}

	if response.StatusCode == http.StatusOK {
		os.Remove(path)
	}
}
