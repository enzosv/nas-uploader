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
	ProgressChan chan float64
	UploadedChan chan FileInfo
}

type FileInfo struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     string `json:"size"`
	UploadID string `json:"upload_id"`
}

func main() {
	err := godotenv.Load()
	ctx := context.Background()

	driveService, err := drive.NewService(ctx)
	if err != nil {
		log.Panic(err)
	}

	http.HandleFunc("/files", ListFilesHandler(driveService))
	http.HandleFunc("/upload", UploadHandler(driveService))
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
			files, err := listFiles(os.Getenv("ROOT"))
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

func UploadHandler(driveService *drive.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var upgrader = websocket.Upgrader{} // use default options
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serveError(w, err, http.StatusInternalServerError)
			return
		}
		defer log.Println("socket closed")
		defer c.Close()

		path := r.URL.Query().Get("path")
		ctx := r.Context()
		channels := Channels{make(chan error), make(chan float64), make(chan FileInfo)}

		go upload(ctx, channels, driveService, path)
		log.Println("uploading", path)
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-channels.ErrChan:
				log.Println(err)
				response := map[string]string{}
				response["error"] = err.Error()
				c.WriteJSON(response)
			case progress := <-channels.ProgressChan:
				response := map[string]float64{}
				response["progress"] = progress
				c.WriteJSON(response)
			case uploaded := <-channels.UploadedChan:
				response := map[string]FileInfo{}
				response["uploaded"] = uploaded
				c.WriteJSON(response)
				log.Println("uploaded", path)
				return
			}
		}
	}
}

func listFiles(root string) ([]FileInfo, error) {
	var files []FileInfo
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
		files = append(files, FileInfo{path, name, byteCountSI(info.Size()), ""})
		return nil
	})
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
	info, _ := file.Stat()

	upf := drive.File{}
	upf.Parents = []string{os.Getenv("FOLDER_ID")}
	upf.Name = info.Name()

	upload := driveService.Files.
		Create(&upf).
		ResumableMedia(ctx, file, info.Size(), mime).
		Fields("webViewLink, id").
		ProgressUpdater(func(current, total int64) {
			progress := float64(current*100) / float64(total)
			channels.ProgressChan <- progress
		})
	df, err := upload.Do()
	if err != nil {
		channels.ErrChan <- err
		return
	}
	channels.UploadedChan <- FileInfo{df.WebViewLink, info.Name(), byteCountSI(info.Size()), df.Id}
}

func listUploaded(driveService *drive.Service) ([]FileInfo, error) {
	list, err := driveService.Files.List().Fields("files(webViewLink, name, id, size)").Do()
	if err != nil {
		return nil, err
	}
	var files []FileInfo
	for _, f := range list.Files {
		files = append(files, FileInfo{f.WebViewLink, f.Name, byteCountSI(f.Size), f.Id})
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

func byteCountSI(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}
