package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"google.golang.org/api/drive/v3"
)

type Channels struct {
	ErrChan      chan error
	ProgressChan chan FileInfo
	UploadedChan chan FileInfo
}

type FileInfo struct {
	Path     string  `json:"path"`
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	UploadID string  `json:"upload_id"`
	Progress float64 `json:"upload_progress"`
}

var uploading []string

func main() {
	err := godotenv.Load()
	ctx := context.Background()

	driveService, err := drive.NewService(ctx)
	if err != nil {
		log.Panic(err)
	}
	channels := Channels{make(chan error), make(chan FileInfo), make(chan FileInfo)}

	http.HandleFunc("/socket", SocketHandler(channels))
	http.HandleFunc("/files", ListFilesHandler(driveService))
	http.HandleFunc("/upload", UploadHandler(ctx, driveService, channels))
	// TODO: delete
	http.Handle("/", http.FileServer(http.Dir("./web")))
	fmt.Println("serving at localhost:8080")
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Panic(err)
	}
}

func serveError(w http.ResponseWriter, err error, status int) {
	response := map[string]string{}
	response["error"] = err.Error()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	json.NewEncoder(w).Encode(response)
}

func ListFilesHandler(driveService *drive.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		errchan := make(chan error)
		fileschan := make(chan []FileInfo)
		uploadschan := make(chan []FileInfo)
		go func() {
			files, err := listFiles(strings.Split(os.Getenv("ROOT"), ","))
			if err != nil {
				errchan <- err
				return
			}
			fileschan <- files
		}()
		go func() {
			files, err := listUploaded(driveService)
			if err != nil {
				errchan <- err
				return
			}
			uploadschan <- files
		}()
		var local []FileInfo
		var online []FileInfo
	loop:
		for {
			select {
			case err := <-errchan:
				log.Println(err)
				serveError(w, err, http.StatusInternalServerError)
				return
			case files := <-fileschan:
				local = files
				if online != nil {
					break loop
				}
			case files := <-uploadschan:
				online = files
				if local != nil {
					break loop
				}
			}
		}
		for _, o := range online {
			found := false
			for i, f := range local {
				if f.Name == o.Name && f.Size == o.Size {
					local[i] = o
					found = true
					break
				}
			}
			if !found {
				local = append(local, o)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err := json.NewEncoder(w).Encode(local)
		if err != nil {
			log.Println(err)
			serveError(w, err, http.StatusInternalServerError)
		}
	}
}

func SocketHandler(channels Channels) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var upgrader = websocket.Upgrader{} // use default options
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println(err)
			serveError(w, err, http.StatusInternalServerError)
			return
		}
		log.Println("socket opened")
		defer log.Println("socket closed")
		defer c.Close()
		for {
			select {
			case err := <-channels.ErrChan:
				log.Println(err)
				response := map[string]string{}
				response["error"] = err.Error()
				c.WriteJSON(response)
			case progress := <-channels.ProgressChan:
				fmt.Printf("%s: %.2f\r", progress.Name, progress.Progress)
				c.WriteJSON(progress)
			case uploaded := <-channels.UploadedChan:
				c.WriteJSON(uploaded)
				// TODO: remove uploaded from uploading
			}
		}
	}
}

func UploadHandler(ctx context.Context, driveService *drive.Service, channels Channels) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		log.Println(path)
		// ctx := r.Context()
		uploading = append(uploading, path)
		go upload(ctx, channels, driveService, path)
		log.Println("uploading", path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		response := map[string]string{}
		response["message"] = "Uploading " + path
		err := json.NewEncoder(w).Encode(response)
		if err != nil {
			log.Println(err)
			serveError(w, err, http.StatusInternalServerError)
		}
	}
}

func listFiles(roots []string) ([]FileInfo, error) {
	var files []FileInfo
	var err error
	for _, root := range roots {
		err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				log.Println(err)
				return nil
			}
			name := info.Name()
			if strings.HasPrefix(name, ".") {
				return nil
			}
			if strings.HasPrefix(path, ".") {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			files = append(files, FileInfo{path, name, info.Size(), "", 0})
			return nil
		})
	}

	return files, err
}

func upload(ctx context.Context, channels Channels, driveService *drive.Service, path string) {
	file, err := os.Open(path)

	if err != nil {
		channels.ErrChan <- err
		return
	}
	defer file.Close()
	mime, err := getMime(file)
	if err != nil {
		channels.ErrChan <- err
		return
	}
	info, err := file.Stat()
	if err != nil {
		channels.ErrChan <- err
		return
	}
	name := info.Name()
	size := info.Size()

	upf := drive.File{}
	upf.Parents = []string{os.Getenv("FOLDER_ID")}
	upf.Name = name

	upload := driveService.Files.
		Create(&upf).
		ResumableMedia(ctx, file, info.Size(), mime).
		Fields("webViewLink, id").
		ProgressUpdater(func(current, total int64) {
			progress := float64(current*100) / float64(total)
			channels.ProgressChan <- FileInfo{path, name, size, "", progress}
		})
	df, err := upload.Do()
	if err != nil {
		channels.ErrChan <- err
		return
	}
	channels.UploadedChan <- FileInfo{df.WebViewLink, name, size, df.Id, 100}
}

func listUploaded(driveService *drive.Service) ([]FileInfo, error) {
	list, err := driveService.Files.List().Fields("files(webViewLink, name, id, size)").Do()
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	for _, f := range list.Files {
		files = append(files, FileInfo{f.WebViewLink, f.Name, f.Size, f.Id, 100})
		// err = driveService.Files.Delete(f.Id).Do()
		// if err != nil {
		// 	log.Panic(err)
		// }
	}
	return files, nil
}

func getMime(ouput *os.File) (string, error) {
	// to sniff the content type only the first
	// 512 bytes are used.
	buf := make([]byte, 512)
	_, err := ouput.Read(buf)
	if err != nil {
		return "", err
	}
	contentType := http.DetectContentType(buf)
	return contentType, nil
}
