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

	"github.com/joho/godotenv"
	"google.golang.org/api/drive/v3"
)

type Channels struct {
	ErrChan      chan error
	ProgressChan chan float64
	UploadedChan chan FileInfo
}

type FileInfo struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size string `json:"size"`
}

func main() {
	err := godotenv.Load()
	ctx := context.Background()

	driveService, err := drive.NewService(ctx)
	if err != nil {
		log.Panic(err)
	}

	http.HandleFunc("/files", ListFilesHandler())
	http.HandleFunc("/upload", UploadHandler(driveService))
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

func ListFilesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		files, err := listFiles(os.Getenv("ROOT"))
		if err != nil {
			log.Println(err)
			serveError(w, err, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		err = json.NewEncoder(w).Encode(files)
		if err != nil {
			log.Println(err)
			serveError(w, err, http.StatusInternalServerError)
		}
	}
}

func UploadHandler(driveService *drive.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Query().Get("path")
		ctx := r.Context()
		channels := Channels{make(chan error), make(chan float64), make(chan FileInfo)}

		go upload(ctx, channels, driveService, path)
		log.Println("uploading", path)
		for {
			select {
			case err := <-channels.ErrChan:
				log.Println(err)
				serveError(w, err, http.StatusInternalServerError)
				return
			case progress := <-channels.ProgressChan:
				fmt.Printf("%.2f%% Uploaded\r", progress)
				//TODO: socket for progress
			case uploaded := <-channels.UploadedChan:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				err := json.NewEncoder(w).Encode(uploaded)
				if err != nil {
					log.Println(err)
					serveError(w, err, http.StatusInternalServerError)
					return
				}
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
		if info.IsDir() {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		files = append(files, FileInfo{path, name, byteCountSI(info.Size())})
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
		Fields("webViewLink").
		ProgressUpdater(func(current, total int64) {
			progress := float64(current*100) / float64(total)
			channels.ProgressChan <- progress
		})
	df, err := upload.Do()
	if err != nil {
		channels.ErrChan <- err
		return
	}
	channels.UploadedChan <- FileInfo{df.WebViewLink, info.Name(), byteCountSI(info.Size())}
}

func listUploaded(driveService *drive.Service) {
	list, err := driveService.Files.List().Fields("files(webViewLink, name, id, parents)").Do()
	if err != nil {
		log.Panic(err)
	}
	for _, f := range list.Files {
		log.Println(f.Name, f.Parents, f.WebViewLink)
		// err = driveService.Files.Delete(f.Id).Do()
		// if err != nil {
		// 	log.Panic(err)
		// }
	}
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
