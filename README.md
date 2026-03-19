# fastTransfer

`fastTransfer` is a DTLS-protected, forward-error-corrected file download tool.

It is designed for high-latency or lossy links where sending redundant shards is
often more effective than waiting on per-packet retransmission. The repository
currently builds one binary, `transferd`, which can run as either:

- a server that exposes local files and directories for download
- a client that lists directories or downloads files from a server

The current implementation is download-only. A client can ask the server to:

- transfer a single file
- list one directory
- recursively walk a directory tree client-side and download matching files

## Main Use Cases

- Pull large media or archive files over unstable UDP paths.
- Recursively mirror a directory tree from a remote host.
- Resume a recursive download by skipping local files whose size already matches.
- Filter recursive downloads by full remote path regex.
- Inspect how a local file would be split into parts for the active FEC engine.

## How It Works

At a high level, the stack is:

1. VLite UDP transport
2. DTLS with PSK authentication
3. A custom request/response protocol
4. FEC-encoded payload transmission

The default FEC engine is native RaptorQ. The older external-process FEC path is
still available through `FEC_ENGINE=exec`.

## Requirements

- Go 1.25 or newer to build from source
- A shared `PSKey` on both client and server
- UDP reachability between client and server

Optional:

- `wirehairutil` or compatible helper if you choose `FEC_ENGINE=exec`

## Build

Build the CLI:

```bash
go build -o transferd ./transferd
```

## Quick Start

Start a server:

```bash
PSKey='replace-with-a-long-random-secret' ./transferd -Address 0.0.0.0:21978
```

List a remote directory:

```bash
PSKey='replace-with-a-long-random-secret' \
./transferd -client -Address example.com:21978 -list -remoteFileName /srv/data
```

Download a single file:

```bash
PSKey='replace-with-a-long-random-secret' \
./transferd -client -Address example.com:21978 \
  -remoteFileName /srv/data/movie.mkv \
  -localFileName ./movie.mkv
```

Recursively download a directory:

```bash
PSKey='replace-with-a-long-random-secret' \
./transferd -client -Address example.com:21978 \
  -recursive \
  -remoteFileName /srv/data/anime \
  -localFileName ./anime
```

Resume a recursive download and filter it:

```bash
PSKey='replace-with-a-long-random-secret' \
./transferd -client -Address example.com:21978 \
  -recursive -resume \
  -filter '/srv/data/anime/.*\\.mkv$' \
  -remoteFileName /srv/data/anime \
  -localFileName ./anime
```

## Command-Line Arguments

All flags are handled by `transferd`.

### Common

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `-Address` | string | `127.0.0.1:13315` | Server bind address in server mode, or server address in client mode. |
| `-client` | bool | `false` | Run as client instead of server. |

### Client Download/List Flags

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `-remoteFileName` | string | `""` | Remote server path to a file or directory. |
| `-localFileName` | string | `""` | Local output path. Semantics depend on mode. |
| `-recvRate` | int | `1000` | Desired receive rate in packets per second. |
| `-list` | bool | `false` | Request a directory listing instead of downloading file data. |
| `-recursive` | bool | `false` | Recursively walk directory trees client-side. |
| `-resume` | bool | `false` | In recursive mode, skip a file if a local regular file already exists with the same size. |
| `-filter` | string | `""` | In recursive mode, only download files whose full remote path matches this regex. Directories are still traversed. |
| `-fromPart` | int | `0` | For non-recursive single-file download only, start at a specific remote part index. |

### Local Part Inspection Flag

| Flag | Type | Default | Meaning |
| --- | --- | --- | --- |
| `-partFile` | bool | `false` | Do not contact a server. Split a local file into transfer parts using the active FEC engine and print SHA-256 plus part index for each part. |

## Environment Variables

### Required

| Variable | Required | Meaning |
| --- | --- | --- |
| `PSKey` | yes | Pre-shared DTLS key used by both client and server. |

### Optional

| Variable | Default | Meaning |
| --- | --- | --- |
| `FEC_ENGINE` | `raptorq` | FEC backend. Supported values: `raptorq`, `exec`. |
| `RAPTORQ_MAX_SOURCE_SYMBOLS` | `4096` | Upper bound for RaptorQ source symbols per part. Lower values reduce memory use and increase part count. Maximum allowed is `56403`. |
| `FEC_BINARY_PATH` | unset | Required only when `FEC_ENGINE=exec`. Path to the external FEC helper. |

## Client Modes and Semantics

### 1. Single-File Download

Default client mode without `-list` and without `-recursive`.

Example:

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -remoteFileName /srv/data/file.bin \
  -localFileName ./file.bin
```

Behavior:

- The remote path must point to a file.
- If the remote file spans multiple transfer parts, the client writes:
  - part 0 to `./file.bin`
  - later parts to `./file.bin.00000001`, `./file.bin.00000002`, and so on
- Use `-fromPart` to restart from a specific remote part number.

This mode does not reconstruct multipart files into one output file.

### 2. Directory Listing

Use `-list`.

Example:

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -list -remoteFileName /srv/data
```

Output format:

- `d name/` for directories
- `- name (12345 bytes)` for files

Notes:

- If the remote path is a file, the client prints a single file entry.
- With `-list -recursive`, recursion is performed client-side by repeatedly
  fetching one directory listing at a time.
- Recursive listing now reuses the same DTLS session across the traversal.

### 3. Recursive Download

Use `-recursive`.

Example:

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -recursive -remoteFileName /srv/data -localFileName ./mirror
```

Behavior:

- If the remote path is a directory, the client lists it, walks children
  client-side, and downloads files one by one.
- If the remote path is a file, the client downloads that file as one
  reconstructed local file.
- Multipart remote files are reassembled into a single local file.
- The client writes to a temporary file in the destination directory and
  renames it into place on success.

Destination rules:

- If `-localFileName` is empty, the output root defaults to the basename of the
  remote path, or `download` for `/`.
- If `-localFileName` is set for a directory download, it is used as the local
  root directory directly.
- If `-recursive` is used on a single remote file and `-localFileName` points to
  an existing local directory, the output file is created inside that directory
  using the remote basename.

Resume rules:

- `-resume` applies only to recursive mode.
- A file is skipped only when the local path already exists as a regular file
  and its size exactly matches the remote size from the listing.
- This is size-only resume. No hash verification is performed.

Filter rules:

- `-filter` applies only in recursive mode.
- The regex is matched against the full remote file path.
- Directories are still traversed even if they do not match the regex.
- When a filter is set, empty destination directories are not created eagerly.
  Parent directories are created only for files that are actually downloaded.

## Examples

### Run a server on all interfaces

```bash
PSKey=... ./transferd -Address 0.0.0.0:21978
```

### Download only MKV files recursively

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -recursive -filter '/media/.*\\.mkv$' \
  -remoteFileName /media -localFileName ./media
```

### Resume a recursive download

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -recursive -resume \
  -remoteFileName /media -localFileName ./media
```

### Recursively list a directory tree

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -list -recursive -remoteFileName /media
```

### Download a single remote file as split parts

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -remoteFileName /big/file.iso -localFileName ./file.iso
```

If the file spans multiple remote parts, you will get:

```text
./file.iso
./file.iso.00000001
./file.iso.00000002
...
```

### Start from part 3 of a split single-file download

```bash
PSKey=... ./transferd -client -Address host:21978 \
  -remoteFileName /big/file.iso -localFileName ./file.iso -fromPart 3
```

### Inspect local part boundaries

```bash
PSKey=... ./transferd -partFile -remoteFileName ./movie.mkv
```

This prints:

```text
<sha256> 00000000
<sha256> 00000001
...
```

The part size is determined by the active FEC engine.

## Progress and Debug Output

The client prints progress and diagnostics to standard error.

Examples of current messages:

- `remote/file.bin: 3/12 parts downloaded`
- `recursive plan: ...`
- `filter check: ...`
- `resume check: ...`
- `download action: ...`

The transfer client also prints a packet summary to standard output when a part
or listing finishes:

```text
Total Receive: 4096, Seq: 5527, Loss: 0.25
```

## Protocol Description

### Transport and Security

- Underlying transport: VLite UDP transport
- Session security: DTLS using `github.com/pion/dtls/v3`
- Authentication: PSK from `PSKey`
- Cipher suite: `TLS_ECDHE_PSK_WITH_AES_128_CBC_SHA256`
- MTU: `1450`
- DTLS handshake timeout: `30s`
- Server-side HelloVerify is disabled to keep the handshake on a single UDP flow

### Session Model

- A client opens a DTLS session to the server.
- Multiple requests can reuse the same DTLS session.
- Each logical request gets a unique `TransferID`.
- `TransferID` prevents stale packets from an earlier request being accepted by
  a later request on the same DTLS connection.

### Request Type Model

There is no explicit "list" or "file" request type in the wire format.

The client sends a path. The server checks that path with `os.Stat`:

- if it is a directory, the response payload type is `list`
- if it is a file, the response payload type is `file`

Recursive behavior is entirely client-side.

### Client-to-Server Message

`Client2Server` fields:

| Field | Meaning |
| --- | --- |
| `Seq` | Client sequence number for the current transfer. |
| `TransferID` | Logical request identifier. |
| `Path` | Requested server-local filesystem path. |
| `FilePart` | Requested remote file part number. |
| `ShardSize` | Requested FEC shard size. The current default is `1300`. |
| `RecvWindow` | Client-advertised number of shards the server may send before more acknowledgements. |
| `RecvRate` | Desired packet rate in packets per second. |

Special behavior:

- When `RecvRate == 0`, the server treats the message as "stop the current
  transfer state for this `TransferID`".
- The client keeps sending updates roughly every `100ms` while a transfer is in
  progress.

### Server-to-Client Message

`Server2Client` fields:

| Field | Meaning |
| --- | --- |
| `ResponseType` | Data or error. |
| `PayloadType` | File payload or directory listing payload. |
| `Seq` | Server sequence number for the current transfer. |
| `TransferID` | Matches the request transfer id. |
| `FileSize` | Size of the current payload body before FEC. |
| `FileTotalParts` | Total number of remote parts for the original file. |
| `ShardSeq` | FEC symbol id. |
| `Data` | FEC shard bytes or error text. |

### Listing Payload

Directory listings are JSON payloads containing:

- `requested_path`
- `root_is_dir`
- `root_size`
- `entries[]`

Each entry includes:

- `path`
- `is_dir`
- `size`

Only directories and regular files are included. Other filesystem types are
ignored.

### File Parting

Files may be split into remote parts before FEC encoding.

- Default shard size: `1300` bytes
- Max part size depends on the active FEC engine
- For RaptorQ, the part size is `RAPTORQ_MAX_SOURCE_SYMBOLS * shard_size`

With the default `RAPTORQ_MAX_SOURCE_SYMBOLS=4096`, the default max remote part
size is:

```text
4096 * 1300 = 5,324,800 bytes
```

This cap exists to keep RaptorQ memory usage bounded.

### FEC Behavior

- The server generates random-access FEC symbols for the payload.
- The client keeps collecting shards until the decoder reports success.
- For payloads smaller than one shard, the implementation uses a small-buffer
  passthrough path instead of invoking the full FEC machinery.

Supported FEC modes:

- `raptorq`: native Go integration using `github.com/xssnick/raptorq`
- `exec`: external helper process controlled through stdin/stdout

### Flow Control, Retry, and Timeouts

- The client watchdog treats a transfer as stalled if no packet arrives for
  `2s`.
- The server stops sending if it does not receive client updates for `2s`.
- Retryable transport failures cause the client to reconnect and retry.
- Recursive listings and recursive downloads reuse a DTLS session and reconnect
  only when that session fails.

## Security Notes

- The server serves raw filesystem paths supplied by the client.
- A client can read any file or directory that the server process user can read.
- Run the server under a restricted account and expose only what that account
  should be able to read.
- Protect the UDP port with firewall rules if appropriate.
- Use a long random `PSKey`.

## Systemd User Service

An example user unit is included at [`transferd.user.service`](./transferd.user.service).

Typical install flow:

```bash
mkdir -p ~/.config/systemd/user
cp ./transferd.user.service ~/.config/systemd/user/transferd.service
systemctl --user daemon-reload
systemctl --user enable --now transferd.service
```

Before enabling it, edit the unit and set a real `PSKey`.

## Current Limitations

- No upload mode
- Resume is size-only, not checksum-based
- Non-recursive single-file download writes multipart files as separate files
- Directory listing recursion is client-side, not streamed from the server
- Paths are server-local filesystem paths, not a virtual namespace
