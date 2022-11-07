## What this is?

- A program made to be run on a NAS that enables you to upload files to the cloud directly from the NAS

### Features

- Upload to Google Drive
- Simple Web UI

## How to

### Requirements

1. A google service account credential file with access to the Google Drive API

- I recommend creating a folder in your personal Google Drive and sharing it to this service account

2. An env file with:

- FOLDER_ID
  - Google Drive Folder ID to upload files to
- ROOT
  - a comma separated list of directories to scan through for selecting files to upload

3. go (only tested with 1.18)

### Running

1. `go get -d`
2. `go run main.go`
3. Go to [localhost:8080](http://localhost:8080)`
