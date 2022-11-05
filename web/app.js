var files;
var ws;
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
          const title = `${name} (${humanFileSize(row.size)})`;
          if (row.upload_id) {
            return `<a href=${row.path}>${title}</a>`;
          }
          if (row.upload_progress > 0) {
            return `Uploading ${title} ${row.upload_progress}%`;
          }
          return `<button onclick="upload('${row.path}'); return false;">${title}</button>`;
        },
      },
    ],
  });
}

async function upload(path) {
  const url = "/upload?path=" + path;
  const response = await fetch(url);
  const data = await response.json();
  if (data.message) {
    const index = files.findIndex((x) => x.path === path);
    var row = files[index];
    row.upload_progress = 0.01;
    files[index] = row;
    $("#table").DataTable().row(index).data(row).invalidate();
  }
}

function socket() {
  // Create WebSocket connection.
  ws = new WebSocket(
    `ws://${window.location.hostname}:${window.location.port}/socket`
  );
  ws.addEventListener("open", function () {
    console.log("socket opened");
  });

  ws.addEventListener("close", function () {
    console.log("socket closed");
  });

  // const row = $("#table").DataTable().row(index);
  ws.addEventListener("message", function (message) {
    console.log(message);
    const data = JSON.parse(message.data);
    const index = files.findIndex(
      (x) => x.size === data.size && x.name === data.name
    );
    files[index] = data;
    $("#table").DataTable().row(index).data(data).invalidate();
  });
}

function humanFileSize(bytes, si = true, dp = 1) {
  const thresh = si ? 1000 : 1024;

  if (Math.abs(bytes) < thresh) {
    return bytes + " B";
  }

  const units = si
    ? ["kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"]
    : ["KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB"];
  let u = -1;
  const r = 10 ** dp;

  do {
    bytes /= thresh;
    ++u;
  } while (
    Math.round(Math.abs(bytes) * r) / r >= thresh &&
    u < units.length - 1
  );

  return bytes.toFixed(dp) + " " + units[u];
}
