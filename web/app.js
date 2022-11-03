var files;
async function getFiles() {
  const response = await fetch("/files");
  files = await response.json();
  $("#table").DataTable({
    paging: false,
    data: files,
    columns: [
      {
        data: "name",
        render: function (name, type, row) {
          if (row.progress) {
            return `Uploading ${name} ${row.progress}%`;
          }
          const index = files.findIndex((x) => x.path === row.path);
          return `<button onclick="upload('${row.path}', ${index}); return false;">${name} (${row.size})</button>`;
        },
      },
    ],
  });
}

function upload(path, index) {
  if (path.includes("drive.google.com")) {
    window.open(path);
    return;
  }
  // Create WebSocket connection.
  const ws = new WebSocket("ws://localhost:8080/upload?path=" + path);
  ws.addEventListener("open", function () {
    console.log("uploading", path);
  });

  ws.addEventListener("close", function () {
    console.log(path, "upload ended");
  });

  const row = $("#table").DataTable().row(index);
  ws.addEventListener("message", function (message) {
    const data = JSON.parse(message.data);
    if (data.error) {
      console.error(data.error);
    }

    if (data.progress) {
      console.log(data.progress);
      var rowdata = files[index];
      rowdata.progress = data.progress;
      row.data(rowdata).invalidate();
    }
    if (data.uploaded) {
      ws.close();
      files[index] = data.uploaded;
      row.data(data.uploaded).invalidate();
    }
  });
}
