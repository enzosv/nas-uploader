package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"google.golang.org/api/drive/v3"
)

const FOLDER_LIMIT int64 = 5e+9

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

func main() {
	err := godotenv.Load()
	ctx := context.Background()

	driveService, err := drive.NewService(ctx)
	if err != nil {
		log.Panic(err)
	}
	channels := Channels{make(chan error), make(chan FileInfo), make(chan FileInfo)}

	http.HandleFunc("/socket", SocketHandler(channels))
	http.HandleFunc("/files", ListFilesHandler(driveService, channels))
	http.HandleFunc("/upload", UploadHandler(ctx, driveService, channels))
	http.HandleFunc("/delete", DeleteHandler(driveService))
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

func DeleteHandler(driveService *drive.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uploadID := r.URL.Query().Get("upload_id")
		err := driveService.Files.Delete(uploadID).Do()
		if err != nil {
			log.Println(err)
			serveError(w, err, http.StatusInternalServerError)
			return
		}
		response := map[string]string{}
		response["upload_id"] = uploadID
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(response)
	}
}

func ListFilesHandler(driveService *drive.Service, channels Channels) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		errchan := make(chan error)
		fileschan := make(chan []FileInfo)
		uploadschan := make(chan []FileInfo)

		var uploading []FileInfo
		go func() {
			for {
				select {
				case <-r.Context().Done():
					break
				case progress := <-channels.ProgressChan:
					found := false
					for i, u := range uploading {
						if u.Path == progress.Path {
							u.Progress = progress.Progress
							uploading[i] = u
							found = true
							break
						}
					}
					if !found {
						uploading = append(uploading, progress)
					}
				}
			}
		}()
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
		for i, l := range local {
			for _, u := range uploading {
				if u.Progress >= 100 {
					continue
				}
				if u.Path == l.Path {
					local[i] = u
				}
			}
		}
		for _, o := range online {
			found := false
			for i, l := range local {
				if o.Name == l.Name && o.Size == l.Size {
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
			}
		}
	}
}

func UploadHandler(ctx context.Context, driveService *drive.Service, channels Channels) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		// ctx := r.Context()
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
	size := info.Size()
	err = pruneUploaded(driveService, size)
	if err != nil {
		channels.ErrChan <- err
		return
	}

	name := info.Name()

	upf := drive.File{}
	upf.Parents = []string{os.Getenv("FOLDER_ID")}
	upf.Name = name

	upload := driveService.Files.
		Create(&upf).
		ResumableMedia(ctx, file, size, mime).
		Fields("webViewLink, id").
		ProgressUpdater(func(current, total int64) {
			progress := float64(current*100) / float64(total)
			channels.ProgressChan <- FileInfo{path, name, total, "", progress}
		})
	df, err := upload.Do()
	if err != nil {
		channels.ErrChan <- err
		return
	}
	channels.UploadedChan <- FileInfo{df.WebViewLink, name, size, df.Id, 100}
}

func pruneUploaded(driveService *drive.Service, required int64) error {
	list, err := driveService.Files.List().Fields("files(name, id, size, mimeType, createdTime)").Do()
	if err != nil {
		return err
	}
	var files []*drive.File
	var consumed int64
	for _, f := range list.Files {
		if f.MimeType == "application/vnd.google-apps.folder" {
			continue
		}
		consumed += f.Size
		files = append(files, f)
	}
	if consumed+required < FOLDER_LIMIT {
		// no need to delete
		return nil
	}
	// delete older first
	sort.Slice(files, func(i, j int) bool {
		a, err := time.Parse(time.RFC3339, files[i].CreatedTime)
		if err != nil {
			return false
		}
		b, err := time.Parse(time.RFC3339, files[j].CreatedTime)
		if err != nil {
			return false
		}
		return a.Unix() < b.Unix()
	})
	for _, f := range files {
		size := f.Size
		err = driveService.Files.Delete(f.Id).Do()
		if err != nil {
			return err
		}
		consumed -= size
		if consumed+required < FOLDER_LIMIT {
			return nil
		}
	}
	// not enough was deleted
	return nil
}

func listUploaded(driveService *drive.Service) ([]FileInfo, error) {
	list, err := driveService.Files.List().Fields("files(webViewLink, name, id, size, mimeType)").Do()
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	for _, f := range list.Files {
		if f.MimeType == "application/vnd.google-apps.folder" {
			continue
		}
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
