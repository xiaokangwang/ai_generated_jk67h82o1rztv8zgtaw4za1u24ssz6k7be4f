# AMP Cache File Distribution Design

This document describes the AMP Cache file distribution system as a protocol and data format. It is intentionally implementation-neutral: another implementation may use any programming language as long as it follows the resource formats, URL rules, encryption rules, manifest formats, and publish/download flows described here.

The goal is to publish an arbitrary file as many AMP-cacheable font resources, then distribute one manifest URL. Anyone with that manifest URL can download enough cached resources, recover the original file with forward error correction, verify hashes, and write the original bytes.

## System Overview

The system has three layers:

1. **AMP resource layer**
   - Publishes one opaque byte payload as a valid AMP-cacheable resource.
   - The file distribution system uses only font resources for file symbols and manifests.
   - Each font resource is addressed by a public origin URL and its corresponding AMP Cache URL.

2. **Origin server layer**
   - Accepts short-lived publish requests.
   - Holds generated resource bytes only while the publish request is active.
   - Serves those resource bytes to AMP Cache origin fetches.
   - Optionally backfills inactive origin misses by fetching the already-cached AMP Cache URL and serving that cached body back to AMP revalidation requests.

3. **File distribution layer**
   - Splits a file into logical chunks.
   - Encodes each chunk into source and recovery symbols with forward error correction.
   - Encrypts every symbol and manifest object.
   - Hides symbol paths with keyed path tokens.
   - Publishes every encrypted object as a font resource.
   - Outputs one root manifest AMP Cache URL with the decryption key in the URL fragment.

The URL fragment is essential. It carries the file decryption key and is not sent in HTTP requests. Sharing the full manifest URL, including the fragment, grants read access to the file.

## Terminology

- **Origin server**: The server controlled by the publisher. It exposes the publish API and public resource URLs.
- **AMP Cache**: The public cache domain, normally `cdn.ampproject.org`.
- **Resource URL**: A public origin URL ending in `.ttf` for file distribution.
- **Cache URL**: The AMP Cache URL derived from a resource URL.
- **Payload**: The opaque byte string carried inside one AMP resource. In file distribution this is encrypted data.
- **Symbol**: One FEC-encoded piece of a file chunk.
- **Chunk**: A contiguous logical slice of the original file.
- **Root manifest**: The top-level encrypted manifest addressed by the final shared URL.
- **Page manifest**: An encrypted manifest page containing metadata for up to 1024 chunks.
- **File access key**: A random 32-byte key embedded in the final URL fragment.
- **Content key**: A derived key used to encrypt and decrypt manifests and symbols.
- **Path key**: A derived key used to generate non-revealing symbol path tokens.

## Default Parameters

These defaults define the current interoperable profile:

| Parameter | Default |
| --- | ---: |
| File chunk size | 64 MiB |
| Symbol payload size before encryption and font wrapping | 512 KiB |
| Minimum recovery symbols per chunk | 16 |
| Minimum total symbols ratio | 1.25x source symbols |
| Manifest chunks per page | 1024 |
| Publish workers | 4 |
| Download/verify symbol workers | 32 |
| Failed-symbol retry rounds | 6 |
| AMP Cache domain | `cdn.ampproject.org` |
| AMP Cache TLS server name | `www.google.com` |
| AMP Cache request User-Agent | `Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.7827.116 Mobile Safari/537.36` |
| AMP Cache request Accept | `application/font-woff2;q=1.0,application/font-woff;q=0.9,*/*;q=0.8` |
| File resource encoding | Font only |
| Font resource extension | `.ttf` |
| Manifest format version | 2 |

The origin server also enforces a maximum encoded resource body size. The configured maximum must include encryption overhead and font wrapper overhead, not only the raw symbol size.

## AMP Cache URL Construction

Given:

- `cache_domain`, normally `cdn.ampproject.org`
- `resource_url`, an absolute `http` or `https` URL
- `encoding`, one of `font`, `image`, or `html`

The file distribution system uses `font`.

1. Parse the resource URL.
2. The resource host must be ASCII.
3. Build the AMP Cache subdomain from the origin hostname:
   - Lowercase the hostname and remove any trailing dot.
   - Replace every hyphen `-` with double hyphen `--`.
   - Replace every dot `.` with hyphen `-`.
   - If the result is no longer than 63 characters, contains at least one dot in the original hostname, and contains only lowercase letters, digits, and hyphens without leading or trailing hyphen, use it.
   - Otherwise use lowercase Base32 without padding of `SHA-256(origin hostname)`.
4. Select the AMP path class:
   - Font: `r`
   - Image: `i`
   - HTML: `c`
5. Build the cache path:
   - Start with `/<class>/`.
   - If the origin resource scheme is `https`, append `s/`.
   - Append the original host, including any port if present.
   - Append the escaped origin path.
   - Preserve the origin query string.

Final form:

```text
https://<cache-subdomain>.<cache-domain>/<class>/[s/]<origin-host><origin-path>[?<origin-query>]
```

For example:

```text
https://hacksaw--hammer-exe-xyz.cdn.ampproject.org/r/s/hacksaw-hammer.exe.xyz/files/<token>.ttf
```

## AMP Cache Request Defaults

Clients that fetch AMP Cache URLs use HTTP GET and should send:

```text
Accept: application/font-woff2;q=1.0,application/font-woff;q=0.9,*/*;q=0.8
User-Agent: Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.7827.116 Mobile Safari/537.36
```

The `Accept` header is applied only to requests whose host is the configured AMP Cache domain or a subdomain of it. Origin-server API requests are not given this cache-specific `Accept` header.

Outbound HTTPS requests to AMP Cache may use TLS domain fronting. In the current profile the URL and HTTP `Host` remain the AMP Cache host, while the TLS server name is `www.google.com`. Setting the TLS server name to an empty value disables domain fronting.

## Origin Server Resource Rules

The origin server has a configured public base URL. A publish request may only create resource URLs that:

- Are absolute `http` or `https` URLs.
- Match the public base URL scheme and host.
- Are under the public base URL path prefix.
- Do not use `/v1/` API paths.
- Do not contain a fragment.
- Use an extension compatible with the declared encoding.

For file distribution, every generated resource URL ends in `.ttf` and uses font encoding.

### Publish API

Publish one payload with:

```text
POST /v1/publish?encoding=font&resource_url=<absolute-origin-resource-url>
Content-Type: application/octet-stream

<payload bytes>
```

The server:

1. Validates the resource URL.
2. Wraps the payload bytes as a valid font resource.
3. Rejects the request if the encoded font resource exceeds the configured resource size limit.
4. Creates an active in-memory session keyed by resource URL and path.
5. Streams newline-delimited JSON events to the client.

The first event is:

```json
{
  "event": "ready",
  "encoding": "font",
  "resource_url": "<origin resource URL>",
  "cache_url": "<derived AMP Cache URL>",
  "sha256": "<SHA-256 of payload bytes>",
  "length": 123,
  "resource_bytes": 456
}
```

While the publish request remains open, a `GET` or `HEAD` to the resource URL serves the generated font bytes. When AMP Cache fetches the origin URL, the server emits a `fetched` event:

```json
{
  "event": "fetched",
  "encoding": "font",
  "resource_url": "<origin resource URL>",
  "cache_url": "<AMP Cache URL>",
  "sha256": "<SHA-256 of payload bytes>",
  "length": 123,
  "fetch_count": 1,
  "user_agent": "<request user agent>"
}
```

After the client confirms that AMP Cache has cached the object, it may call:

```text
POST /v1/confirm?resource_url=<origin resource URL>&sha256=<payload SHA-256>
```

The server then emits:

```json
{
  "event": "cached",
  "encoding": "font",
  "resource_url": "<origin resource URL>",
  "cache_url": "<AMP Cache URL>",
  "sha256": "<SHA-256 of payload bytes>",
  "length": 123
}
```

For high-volume file publishing, the client uses a fast path: it waits only until the origin server observes the first AMP Cache fetch, then closes the publish session and moves on. It verifies cache recoverability after all symbols and manifests are uploaded.

### Resource Response Headers

Active resource responses use:

```text
Content-Type: font/ttf
Content-Length: <encoded font byte length>
Cache-Control: public, max-age=31536000
ETag: "ampcache-<server-keyed-path-tag>"
Access-Control-Allow-Origin: *
```

The CORS header is required for font resources. The `immutable` cache-control extension is intentionally not sent.

The origin server generates an ETag from the request path and query using a server-local keyed hash. If a later cacheable resource request includes `If-None-Match` with the expected ETag, the origin returns `304 Not Modified` with the normal cache headers. This ETag-based 304 path works even when the resource is no longer active and backfill is disabled, because the resource content is immutable once published.

### Backfill on Origin Miss

After a publish session closes, the origin server normally no longer has local resource bytes. To support AMP Cache stale revalidation, an origin server may enable backfill:

1. On an inactive resource miss, infer the encoding from the resource extension.
2. Reconstruct the absolute public resource URL from the request path and query.
3. Derive the AMP Cache URL.
4. Fetch that AMP Cache URL.
5. Require HTTP 200 from AMP Cache.
6. Decode the returned resource as a valid system resource.
7. If decoding succeeds and the body is within the resource size limit, serve the cached resource body back to the requester.

Backfilled responses should include the normal resource headers plus:

```text
X-Ampcache-Backfill: hit
```

Backfill is skipped when the incoming user agent contains `Google-AMPHTML`. This avoids a revalidation loop where AMP Cache asks the origin, and the origin asks AMP Cache for the same resource.

If backfill is disabled, skipped, returns 404 or 410, or otherwise fails for a syntactically cacheable resource URL with a known extension, the origin returns:

```text
503 Service Unavailable
Cache-Control: no-store
Retry-After: 60
```

Returning 503 instead of 404 tells AMP Cache that the origin is temporarily unavailable rather than proving that the object is gone, which helps stale cache entries remain eligible to be served. Non-cacheable paths still return normal 404 responses.

Backfill can extend or refresh AMP Cache lifetime, so it should be disabled for idle-expiry experiments.

## Font Resource Wrapper

The file distribution system stores encrypted payload bytes inside valid TrueType font files.

The wrapper must satisfy AMP Cache font validation and must preserve payload bytes exactly after decoding. Existing URLs produced by this project use the glyph-coordinate encoding described below. Older resources may contain a private `AMPC` table; decoders may support that legacy form, but new publishers should not emit it.

### Font Payload Stream

Before storing bytes in the font, construct:

```text
4-byte big-endian unsigned payload length
payload bytes
```

This stream is then packed into 15-bit unsigned values:

1. Read the stream as a big-endian bit sequence.
2. Split it into groups of 15 bits.
3. The final group is zero-padded on the right if needed.
4. Each group becomes one unsigned coordinate value in the range `0..32767`.

The 15-bit values are stored as x/y coordinate pairs in simple glyphs.

### Glyph Layout

The font contains:

- Glyph 0: a simple visible placeholder glyph.
- Glyphs 1..N: data glyphs.

Each data glyph:

- Is a simple glyph with one contour.
- Contains up to 4096 points.
- Has at least 3 points, padding missing coordinate values with zero.
- Stores payload values as alternating x and y coordinates:
  - point 0 x = value 0
  - point 0 y = value 1
  - point 1 x = value 2
  - point 1 y = value 3
  - and so on.
- Uses signed 16-bit coordinate deltas in the TrueType `glyf` table.
- Uses repeated simple flags for all points.

To decode:

1. Read the font's `maxp` table to get glyph count.
2. Read `head.indexToLocFormat`.
3. Read `loca` offsets.
4. For glyphs 1..N, read non-empty simple glyphs from `glyf`.
5. Decode their absolute x and y coordinates.
6. Reject coordinates outside `0..32767`.
7. Append coordinates as 15-bit values.
8. Unpack 15-bit values back into bytes.
9. The first 4 bytes are the payload length; return exactly that many following bytes.

### Required Font Tables

New font resources should be valid SFNT/TrueType files with version `0x00010000` and these tables:

- `OS/2`
- `cmap`
- `glyf`
- `head`
- `hhea`
- `hmtx`
- `loca`
- `maxp`
- `name`
- `post`

Tables are sorted by tag in the SFNT directory. Every table is padded to a four-byte boundary. Checksums are standard SFNT checksums, and `head.checkSumAdjustment` is set so the whole font checksum equals the TrueType magic target.

The `cmap` table maps private-use code points beginning at `U+E000` to data glyphs. The exact text appearance of the font is unimportant; AMP Cache only needs it to be structurally valid.

## File Encryption

Every file-distribution object is encrypted before it is wrapped as a font resource.

### File Access Key

Publishing generates a random 32-byte file access key. The final shared manifest URL appends this key and the root manifest hash as an unpadded Base64URL fragment:

```text
<root manifest AMP Cache URL>#k=<base64url(file access key)>&h=<base64url(sha256(root encrypted manifest payload))>
```

The fragment is not sent to servers during HTTP requests. Anyone with the full URL can decrypt the file. The `h` value binds the shared URL to the exact decoded encrypted root manifest payload and prevents an origin or cache from silently swapping the root manifest bytes for that URL.

### Derived Keys

Use HMAC-SHA-256 with the file access key as the HMAC key:

| Derived key | HMAC message |
| --- | --- |
| Content key | `ampcache-file-content-v1` |
| Path key | `ampcache-file-path-v1` |

Both derived keys are 32 bytes.

### Object Encryption Format

Encrypt every root manifest, page manifest, and symbol payload with AEAD encryption using the content key.

The encrypted object byte string is:

```text
ASCII "AMPFENC1"
random nonce
AEAD ciphertext and authentication tag
```

Current profile:

- Cipher: AES-256-GCM
- Nonce: 12 random bytes
- Authentication tag: standard GCM tag
- Additional authenticated data: object-specific string described below

AAD values:

| Object | AAD |
| --- | --- |
| Root manifest | `ampfile:root` |
| Page manifest | `ampfile:page:<run-id>/m/<8-digit-page-index>` |
| Symbol | `ampfile:symbol:<run-id>/c/<8-digit-chunk-index>/s/<8-digit-symbol-index>` |

Chunk, page, and symbol indexes are zero-based and decimal, padded to exactly eight digits in logical IDs.

AEAD authentication is the primary check that a downloaded object belongs to the requested file and logical position. A wrong resource, wrong key, wrong path token, or corrupt body must fail authentication.

## Path Masking

The system must not reveal chunk or symbol indexes through resource paths.

The run ID should be unique for each publish. The current profile uses the prefix `run-` followed by 16 URL-safe random characters, but compatible decoders treat the run ID as opaque text from the root manifest.

### Symbol Paths

For each symbol, define the logical ID:

```text
<run-id>/c/<8-digit-chunk-index>/s/<8-digit-symbol-index>
```

The public path token is:

```text
Base64URL-no-padding(HMAC-SHA-256(path key, logical ID))
```

The token is 43 URL-safe characters.

The symbol origin URL is:

```text
<resource-base>/<token>.ttf
```

The corresponding AMP Cache URL is derived by the AMP Cache URL construction rules.

### Manifest Paths

Manifest paths are random rather than derived from manifest indexes:

- A page manifest path token is 32 random bytes encoded as unpadded Base64URL.
- The root manifest path token is also 32 random bytes encoded as unpadded Base64URL.
- These tokens are also 43 URL-safe characters.

This gives manifest and symbol URLs the same visible shape:

```text
<resource-base>/<43-character-token>.ttf
```

The manifest URL shown to the user is the root manifest AMP Cache URL with `#k=<file access key>&h=<root manifest hash>` appended.

## File Chunking and FEC

Publishing splits the input file into contiguous chunks.

For each chunk:

```text
chunk offset = chunk index * chunk size
chunk length = min(chunk size, remaining file bytes)
source symbols = ceil(chunk length / symbol size)
```

If the file is empty, chunk count is zero and the root manifest has no pages.

### Total Symbol Count

For initial publish and repair target sizing, the client computes a target symbol count for each non-empty chunk:

```text
by minimum = source symbols + minimum recovery symbols
by ratio = ceil(source symbols * FEC total ratio)
total symbols = max(by minimum, by ratio)
```

With defaults:

```text
total symbols = max(source + 16, ceil(source * 1.25))
```

The root manifest does not store these policy values. The page manifest records the exact inclusive symbol index ranges that were uploaded for each chunk.

### Symbol Encoding

For chunks larger than one symbol:

1. Feed the chunk into a Wirehair-compatible fountain/FEC encoder.
2. For every symbol index from `0` to `total symbols - 1`, generate the symbol with that numeric ID.
3. Each symbol is at most `symbol size` bytes.

For chunks whose length is less than or equal to one symbol:

1. Every symbol payload is the entire chunk.
2. Any one valid symbol is enough to recover the chunk.

After symbol generation:

1. Encrypt the symbol using the content key and symbol AAD.
2. Publish the encrypted bytes as a font resource at the masked symbol URL.

## Manifest Format

Manifest data is encoded as deterministic, definite-length CBOR using only:

- Unsigned integers
- Byte strings
- UTF-8 text strings
- Arrays

Integer encodings use the shortest valid CBOR length. Multibyte integer payloads are big-endian. Text and byte-string lengths are byte counts.

New manifests are version `2`. Version `1` manifests are still readable for compatibility.

### Root Manifest

The version `2` root manifest is a CBOR array with 12 fields in this exact order:

| Index | Field | Type | Description |
| ---: | --- | --- | --- |
| 0 | version | unsigned integer | Must be `2` |
| 1 | run ID | text | Random run identifier |
| 2 | file name | text | Base name of original file |
| 3 | file size | unsigned integer | Original file byte length |
| 4 | file SHA-256 | byte string | 32-byte hash of original file |
| 5 | resource base | text | Origin URL prefix used for generated resources |
| 6 | cache domain | text | AMP Cache domain |
| 7 | chunk size | unsigned integer | Logical chunk size |
| 8 | symbol size | unsigned integer | Symbol size before encryption/font wrapping |
| 9 | chunk count | unsigned integer | Total chunks |
| 10 | chunks per page | unsigned integer | Normally `1024` |
| 11 | pages | array | Page manifest references |

Each page reference is a CBOR array:

| Index | Field | Type |
| ---: | --- | --- |
| 0 | page index | unsigned integer |
| 1 | page AMP Cache URL | text |

The root manifest is encrypted with AAD `ampfile:root`, wrapped as a font resource, published to a random root path, and shared as:

```text
<root manifest AMP Cache URL>#k=<base64url(file access key)>&h=<base64url(sha256(root encrypted manifest payload))>
```

### Page Manifest

A page manifest is a CBOR array with 5 fields:

| Index | Field | Type | Description |
| ---: | --- | --- | --- |
| 0 | version | unsigned integer | Must be `2` |
| 1 | run ID | text | Must match root run ID |
| 2 | page index | unsigned integer | Must match the root page reference |
| 3 | first chunk | unsigned integer | First chunk index in this page |
| 4 | chunks | array | Chunk records |

Each version `2` chunk record is a CBOR array:

| Index | Field | Type | Description |
| ---: | --- | --- | --- |
| 0 | chunk index | unsigned integer | Zero-based chunk index |
| 1 | offset | unsigned integer | Byte offset in original file |
| 2 | length | unsigned integer | Chunk byte length |
| 3 | SHA-256 | byte string | 32-byte hash of plaintext chunk |
| 4 | symbol ranges | array | Inclusive uploaded symbol index ranges |

Each symbol range is a CBOR array:

| Index | Field | Type | Description |
| ---: | --- | --- | --- |
| 0 | start symbol | unsigned integer | Inclusive first symbol index |
| 1 | end symbol | unsigned integer | Inclusive final symbol index, at most `0xefffffff` |

For an initial publish, each non-empty chunk normally has one range, `[0, total symbols - 1]`. Repaired manifests append additional ranges and preserve all existing ranges.

Version `1` root manifests contain the old `minimum recovery symbols` and `FEC total millis` fields. When reading v1, a compatible implementation synthesizes each chunk range as `[0, total symbols - 1]`.

A page manifest is encrypted with AAD:

```text
ampfile:page:<run-id>/m/<8-digit-page-index>
```

It is wrapped as a font resource and published to a random page path. Its AMP Cache URL is stored in the root manifest.

## Publish Flow

Publishing an arbitrary file follows this sequence:

1. Validate inputs:
   - Server URL
   - Resource base URL
   - Chunk size
   - Symbol size
   - FEC parameters
   - Resource size limit on the origin server
2. Generate:
   - Random run ID
   - Random 32-byte file access key
   - Content key
   - Path key
3. Open the input file.
4. For each chunk:
   - Read only that chunk range from the file.
   - Update the full-file SHA-256.
   - Record chunk offset, length, and chunk SHA-256.
   - Compute source and total symbol counts.
   - Record the uploaded symbol range `[0, total symbols - 1]`.
   - For every symbol index:
     - Generate the FEC symbol.
     - Encrypt it with symbol AAD.
     - Compute its masked path token.
     - Publish it as a font resource.
5. Split chunk metadata into pages of up to 1024 chunks.
6. For each page:
   - Encode the page manifest.
   - Encrypt it.
   - Publish it at a random manifest URL.
   - Store the page AMP Cache URL in the root manifest.
7. Encode and encrypt the root manifest.
8. Publish the root manifest at a random root URL.
9. Output the root manifest AMP Cache URL with `#k=<file access key>&h=<root manifest hash>`.
10. Unless disabled, verify recoverability:
    - Fetch the root manifest and pages through AMP Cache.
    - Download symbols through AMP Cache.
    - Recover every chunk.
    - Check every chunk SHA-256.
    - Check final file SHA-256.

The publisher may upload resources using the fast origin-fetch mode: a resource upload is considered complete once AMP Cache has fetched it from origin at least once. This avoids keeping thousands of publish sessions open until every resource is fully verified.

## Verify Flow

Verification checks whether a manifest URL is already recoverable from AMP Cache.

1. Parse the manifest URL and extract the file access key and optional root manifest hash from the fragment.
2. Fetch the root manifest AMP Cache URL.
3. Decode the font wrapper.
4. If the fragment has `h`, verify SHA-256 of the decoded encrypted root manifest payload before decryption.
5. Decrypt the root manifest with the content key.
6. Fetch and decrypt all page manifests.
7. For each chunk:
   - Expand and deduplicate all symbol indexes from the chunk symbol ranges.
   - Derive symbol URLs from run ID, chunk index, symbol indexes, and path key.
   - Fetch symbols concurrently from AMP Cache.
   - Decode each font wrapper.
   - Decrypt each symbol with its symbol AAD.
   - Feed successful symbols into the FEC decoder.
   - Continue through the full first symbol set so the result can report how many symbols succeeded or failed.
   - Retry only retryable failed symbol URLs after all other URLs have been tried. Successful symbols are retained and are not downloaded again.
   - Verify chunk SHA-256.
8. Verify the final file SHA-256 by concatenating recovered chunks in order.

Verification succeeds when every chunk recovers and the final file hash matches. It may still return warnings when some symbols failed to download, because FEC recovery only needs enough valid symbols. The default verification profile allows 6 failed-symbol retry rounds. Verification does not allow unlimited symbol retries.

## Repair Flow

Repair creates a new manifest URL without deleting or rewriting old symbol ranges.

1. Load the existing manifest and preserve its file access key, run ID, resource base, cache domain, and existing chunk ranges.
2. For each chunk, try to recover bytes from the currently available cached symbols.
3. If a chunk cannot be recovered and a source file is supplied, verify the source file size and SHA-256 against the root manifest and read that chunk from the source.
4. Compute the repair target symbol count from the repair ratio flags.
5. Upload enough new symbols to bring the chunk up to the repair target, choosing a random non-overlapping inclusive range whose end is at most `0xefffffff`.
6. Append the new range to the chunk record. Keep all old ranges, even ranges that failed verification.
7. Publish new page manifests for pages whose chunks gained ranges, reuse unchanged page manifest URLs, then publish a new root manifest URL with a fresh `h=` hash.

Verification succeeds if every chunk recovers and the full-file hash matches. It may still report warnings if some symbols failed to download, because recovery only requires enough valid symbols.

## Download Flow

Downloading is the same as verification, with output writing:

1. Load and decrypt root and page manifests.
2. Recover chunks in ascending chunk index order.
3. For each recovered chunk:
   - Download symbols concurrently.
   - Stop downloading more symbols for that chunk once the chunk is recoverable.
   - Retry only retryable failed symbol URLs between rounds. Successfully downloaded symbols are kept in the chunk decoder and are not fetched again.
   - Verify chunk SHA-256.
   - Append plaintext bytes to a temporary output file.
   - Update the full-file hash.
4. After all chunks are recovered:
   - Verify the full-file SHA-256 from the root manifest.
   - Atomically move the temporary file to the requested output path.

Completed chunks are not retried. If a later chunk cannot recover, the download fails at that chunk without re-downloading earlier chunks in the same process. The default downloader allows 6 failed-symbol retry rounds; an implementation may expose an unlimited mode that keeps retrying failed symbol URLs until the chunk recovers, the caller cancels, or a timeout expires.

Manifest loading and symbol loading treat transient cache and network failures as retryable. Retryable cases include HTTP 404, 408, 429, 500, 502, 503, 504, timeouts, connection resets, unexpected EOF, and temporary network failures. AEAD authentication failures, FEC decode errors, hash mismatches, malformed manifests, and malformed font wrappers are not treated as successful downloads; authentication failures are not useful to retry because they indicate the wrong bytes for the requested logical object.

## Compatibility Requirements

An independent implementation can publish URLs that this project can download if it follows all of these rules:

1. Use the same AMP Cache URL construction.
2. Use `.ttf` resource URLs for file symbols and manifests.
3. Encode encrypted object bytes with the glyph-coordinate font wrapper.
4. Generate one random 32-byte file access key and put it in the final URL fragment as `#k=<base64url>&h=<base64url-root-manifest-hash>`.
5. Derive content and path keys with the specified HMAC labels.
6. Encrypt root manifest, page manifests, and symbols with the specified AEAD format and AAD strings.
7. Use the same symbol logical IDs and path token derivation.
8. Use compatible Wirehair FEC symbol generation and recovery.
9. Encode root and page manifests as the specified CBOR arrays.
10. Include the exact uploaded inclusive symbol ranges in every non-empty chunk record.
11. Publish all resources through AMP Cache and output the root manifest cache URL with key and root-hash fragment.

An independent implementation can download URLs created by this project if it follows all of these rules:

1. Parse and remove the key and root-hash fragment before HTTP requests.
2. Decode root and page font resources from AMP Cache.
3. If `h` is present, verify the decoded encrypted root manifest payload hash.
4. Decrypt manifests with the content key and correct AAD.
5. Expand symbol indexes from each chunk's inclusive symbol ranges.
6. Reconstruct symbol URLs from manifest metadata and the path key.
7. Decode and decrypt symbols independently.
8. Feed enough valid symbols into a compatible Wirehair decoder.
9. Verify chunk hashes and final file hash.

## Security and Privacy Model

- AMP Cache and passive observers see encrypted file data, encrypted manifests, and random-looking paths.
- The origin server for file publishing receives encrypted objects, not original file bytes.
- The final manifest URL contains the decryption key and root manifest hash in the fragment. Anyone who sees the full URL can decrypt the file.
- If the URL is copied without the fragment, the file is not recoverable.
- Path masking hides chunk and symbol numbers from URLs, but object size and timing may still reveal coarse information.
- AEAD authentication prevents silent substitution of wrong symbols or manifests.
- SHA-256 chunk and file hashes detect FEC or implementation errors after decryption.

## Operational Notes

- AMP Cache retention is best-effort and may depend on size, access pattern, validation behavior, and cache policy.
- Frequent access can refresh or extend cache lifetime. Idle-expiry testing should use separate URLs for each idle period and avoid backfill.
- Backfill helps AMP stale revalidation but can contaminate cache lifetime measurements.
- Outbound HTTPS requests to AMP Cache may use domain fronting and should use the AMP Cache request headers described above.
- Rate limiting is expected. Download and verification clients should classify 429 separately from 404 and may retry.
- The resource size limit must be configured high enough for the encoded font body. A 512 KiB plaintext symbol grows after encryption and font wrapping.
- The post-upload verification step is important. A publish command should not assume that an origin fetch means all resources are immediately usable from AMP Cache.

## Minimal End-to-End Example

Publishing:

1. Split `example.bin` into 64 MiB chunks.
2. Generate file access key and run ID.
3. Encode and publish all encrypted symbols as masked `.ttf` resources.
4. Encode and publish encrypted page manifests.
5. Encode and publish encrypted root manifest.
6. Print:

```text
https://<origin-cache-subdomain>.cdn.ampproject.org/r/s/<origin-host>/<resource-base>/<root-random-token>.ttf#k=<file-access-key>&h=<root-manifest-hash>
```

Downloading:

1. Fetch the root URL without the fragment.
2. Use the fragment key to decrypt the root manifest.
3. Fetch and decrypt page manifests.
4. Derive symbol URLs.
5. Fetch enough symbols for each chunk.
6. Recover chunks with FEC.
7. Verify hashes.
8. Write the original file.
