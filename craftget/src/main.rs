use std::convert::Infallible;
use std::fs;
use std::future::{Future, poll_fn};
use std::io::{self, BufRead, Read, Write};
use std::net::TcpStream as StdTcpStream;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::sync::Arc;
use std::task::{Context as TaskContext, Poll};
use std::time::Duration;

use anyhow::{Context, Result, anyhow, bail};
use base64::Engine;
use base64::engine::general_purpose::STANDARD as BASE64;
use clap::{ArgAction, Parser};
use hyper::body::{Body, Bytes, Frame, Incoming, SizeHint};
use hyper::header::{ACCEPT_ENCODING, CONTENT_LENGTH, USER_AGENT};
use hyper::{Method, Request, Uri};
use rustls::client::{EchConfig, EchMode};
use rustls::pki_types::{EchConfigListBytes, ServerName};
use rustls::{ClientConfig, RootCertStore};
use rustls::{Connection, craft};
use rustls_util::{Stream, complete_io};
use serde::Serialize;
use url::Url;

const DEFAULT_FINGERPRINT: &str = "chrome_112";
const MAX_BODY_BYTES: usize = 2048;
const DIAL_TIMEOUT: Duration = Duration::from_secs(15);
const FINGERPRINTS: &[&str] = &[
    "chrome_108",
    "chrome_112",
    "chromium_144",
    "chrome_148",
    "craftlsmaxxing",
    "firefox_105",
    "firefox_140",
    "safari_17_1",
    "rustls_test",
];

#[derive(Parser)]
#[command(about = "uget-like HTTPS GET client using craftls fingerprints")]
struct Args {
    /// craftls ClientHello fingerprint to use.
    #[arg(long = "fp")]
    fingerprint: Option<String>,

    /// TCP address to connect to, overriding the URL host and port.
    #[arg(long = "connect-to")]
    connect_to: Option<String>,

    /// TLS SNI and certificate name to verify, overriding the URL host.
    #[arg(long = "tls-server-name")]
    tls_server_name: Option<String>,

    /// Whether to send SNI in the ClientHello.
    #[arg(long = "send-sni", default_value_t = true, action = ArgAction::Set)]
    send_sni: bool,

    /// Shorthand for --send-sni false.
    #[arg(long = "no-sni")]
    no_sni: bool,

    /// Base64 ECHConfigList to use for real ECH instead of DNS HTTPS/SVCB lookup.
    #[arg(long = "ech-key", alias = "ech-config")]
    ech_key: Option<String>,

    /// File containing a base64 ECHConfigList, or raw ECHConfigList bytes.
    #[arg(long = "ech-key-file", alias = "ech-config-file")]
    ech_key_file: Option<PathBuf>,

    /// Whether ECH should force the client config to TLS 1.3 only.
    #[arg(long = "ech-force-tls13", action = ArgAction::Set)]
    ech_force_tls13: Option<bool>,

    /// Print available fingerprint names and exit.
    #[arg(long = "list-fps")]
    list_fps: bool,

    /// Read the full HTTP response body instead of only the first 2048 bytes.
    #[arg(long = "full-body")]
    full_body: bool,

    /// URL to fetch.
    url: Option<String>,
}

#[derive(Default, Serialize)]
struct Output {
    body: String,
    status_code: u16,
    alpn: String,
}

#[derive(Default, Serialize)]
struct ErrorOutput {
    message: String,
}

#[derive(Serialize)]
struct Input {
    url: String,
    client_hello_id: String,
    tcp_connect_to: String,
    #[serde(rename = "tls_servername")]
    tls_server_name: String,
    send_sni: bool,
    ech_enabled: bool,
    ech_force_tls13: bool,
    full_body: bool,
}

#[derive(Serialize)]
struct Summary {
    input: Input,
    output: Output,
    error: ErrorOutput,
}

fn main() -> Result<()> {
    let _ = env_logger::try_init();

    let args = Args::parse();

    if args.list_fps {
        for fingerprint in FINGERPRINTS {
            println!("{fingerprint}");
        }
        return Ok(());
    }

    let url = args.url.context("provide a url")?;
    let uri = Url::parse(&url).with_context(|| format!("can't parse {url}"))?;
    let tcp_connect_to = args
        .connect_to
        .unwrap_or_else(|| default_connect_to(&uri).unwrap_or_default());
    let tls_server_name = args
        .tls_server_name
        .unwrap_or_else(|| default_tls_server_name(&uri).unwrap_or_default());
    let client_hello_id = args.fingerprint.unwrap_or_default();
    let send_sni = args.send_sni && !args.no_sni;
    let ech_config_list =
        load_ech_config_list(args.ech_key.as_deref(), args.ech_key_file.as_deref())?;
    let ech_enabled = ech_config_list.is_some();

    let fingerprint_name = if client_hello_id.is_empty() {
        DEFAULT_FINGERPRINT
    } else {
        client_hello_id.as_str()
    };
    let fingerprint = fingerprint_builder(fingerprint_name).ok_or_else(|| {
        anyhow!(
            "invalid fingerprint {fingerprint_name:?}; use --list-fps to see supported fingerprints"
        )
    })?;
    let ech_force_tls13 = args
        .ech_force_tls13
        .or_else(|| fingerprint.ech_force_tls13())
        .unwrap_or(true);

    let input = Input {
        url,
        client_hello_id: client_hello_id.clone(),
        tcp_connect_to,
        tls_server_name,
        send_sni,
        ech_enabled,
        ech_force_tls13,
        full_body: args.full_body,
    };

    let summary = match http_get(
        &uri,
        fingerprint,
        &input.tcp_connect_to,
        &input.tls_server_name,
        input.send_sni,
        ech_config_list,
        ech_force_tls13,
        body_read_mode(input.full_body),
    ) {
        Ok(output) => Summary {
            input,
            output,
            error: ErrorOutput::default(),
        },
        Err(err) => Summary {
            input,
            output: Output::default(),
            error: ErrorOutput {
                message: format!("{err:#}"),
            },
        },
    };

    serde_json::to_writer(io::stdout().lock(), &summary)?;
    println!();
    Ok(())
}

fn http_get(
    uri: &Url,
    fingerprint: craft::FingerprintBuilder,
    tcp_connect_to: &str,
    tls_server_name: &str,
    send_sni: bool,
    ech_config_list: Option<EchConfigListBytes<'static>>,
    ech_force_tls13: bool,
    body_read_mode: BodyReadMode,
) -> Result<Output> {
    let mut config = build_client_config(fingerprint, send_sni, ech_config_list, ech_force_tls13)?;
    config.enable_sni = send_sni;

    let server_name = ServerName::try_from(tls_server_name)
        .map_err(|err| anyhow!("invalid TLS server name {tls_server_name:?}: {err:?}"))?
        .to_owned();
    let mut conn = Arc::new(config)
        .connect(server_name)
        .build()
        .context("failed to build craftls client")?;
    let mut sock = StdTcpStream::connect(tcp_connect_to)
        .with_context(|| format!("failed to connect to {tcp_connect_to}"))?;
    sock.set_read_timeout(Some(DIAL_TIMEOUT))?;
    sock.set_write_timeout(Some(DIAL_TIMEOUT))?;

    complete_io(&mut sock, &mut conn).context("TLS handshake failed")?;
    let alpn = conn
        .alpn_protocol()
        .map(|protocol| String::from_utf8_lossy(protocol.as_ref()).into_owned())
        .unwrap_or_default();
    let (status_code, body) = match alpn.as_str() {
        "h2" => {
            read_http2_response(conn, sock, uri, body_read_mode).context("HTTP/2 request failed")?
        }
        "" | "http/1.1" => {
            let mut tls = Stream::new(&mut conn, &mut sock);
            write_http1_request(&mut tls, uri)?;
            read_http1_response(&mut tls, body_read_mode)?
        }
        _ => bail!("unsupported ALPN {alpn:?}"),
    };

    Ok(Output {
        body: BASE64.encode(body),
        status_code,
        alpn,
    })
}

fn build_client_config(
    fingerprint: craft::FingerprintBuilder,
    send_sni: bool,
    ech_config_list: Option<EchConfigListBytes<'static>>,
    ech_force_tls13: bool,
) -> Result<ClientConfig> {
    let root_store = RootCertStore {
        roots: webpki_roots::TLS_SERVER_ROOTS.into(),
    };
    let provider = match (ech_config_list.is_some(), ech_force_tls13) {
        (true, true) => rustls_aws_lc_rs::DEFAULT_TLS13_PROVIDER,
        _ => rustls_aws_lc_rs::DEFAULT_PROVIDER,
    };
    let mut builder = ClientConfig::builder(provider.into());

    if let Some(ech_config_list) = ech_config_list {
        let ech_config = EchConfig::new(
            ech_config_list,
            rustls_aws_lc_rs::hpke::ALL_SUPPORTED_SUITES,
        )
        .context("invalid ECH config list")?
        .with_tls13_only(ech_force_tls13);
        builder = builder.with_ech(EchMode::Enable(ech_config));
    }

    let mut config = builder
        .with_root_certificates(root_store)
        .with_no_client_auth()
        .context("failed to build craftls client config")?
        .with_fingerprint(fingerprint);
    config.enable_sni = send_sni;
    Ok(config)
}

fn load_ech_config_list(
    ech_key: Option<&str>,
    ech_key_file: Option<&Path>,
) -> Result<Option<EchConfigListBytes<'static>>> {
    match (ech_key, ech_key_file) {
        (None, None) => Ok(None),
        (Some(_), Some(_)) => bail!("use only one of --ech-key or --ech-key-file"),
        (Some(ech_key), None) => decode_ech_key_text(ech_key).map(Some),
        (None, Some(path)) => decode_ech_key_file(path).map(Some),
    }
}

fn decode_ech_key_file(path: &Path) -> Result<EchConfigListBytes<'static>> {
    let bytes = fs::read(path)
        .with_context(|| format!("failed to read ECH config from {}", path.display()))?;
    match std::str::from_utf8(&bytes) {
        Ok(text) => decode_ech_key_text(text),
        Err(_) => Ok(EchConfigListBytes::from(bytes)),
    }
}

fn decode_ech_key_text(input: &str) -> Result<EchConfigListBytes<'static>> {
    let mut text = input.trim();
    if let Some((_, value)) = text.split_once("ech=") {
        text = value;
    }
    let text = text
        .trim_matches(|ch: char| ch == '"' || ch == '\'')
        .split(|ch: char| ch.is_ascii_whitespace() || ch == ',' || ch == ';')
        .next()
        .unwrap_or_default();
    let base64 = text
        .chars()
        .filter(|ch| !ch.is_ascii_whitespace())
        .collect::<String>();
    let bytes = BASE64
        .decode(base64.as_bytes())
        .context("failed to decode ECH config list base64")?;
    Ok(EchConfigListBytes::from(bytes))
}

fn write_http1_request(stream: &mut impl Write, uri: &Url) -> Result<()> {
    let target = request_target(uri);
    let host = host_header(uri)?;
    write!(
        stream,
        "GET {target} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\nAccept-Encoding: identity\r\nUser-Agent: craftget/0.0.1\r\n\r\n"
    )?;
    stream.flush()?;
    Ok(())
}

fn read_http2_response(
    conn: rustls::ClientConnection,
    sock: StdTcpStream,
    uri: &Url,
    body_read_mode: BodyReadMode,
) -> Result<(u16, Vec<u8>)> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_io()
        .build()
        .context("failed to build tokio runtime for hyper HTTP/2")?;
    runtime.block_on(read_http2_response_async(
        conn,
        sock,
        uri.clone(),
        body_read_mode,
    ))
}

async fn read_http2_response_async(
    conn: rustls::ClientConnection,
    sock: StdTcpStream,
    uri: Url,
    body_read_mode: BodyReadMode,
) -> Result<(u16, Vec<u8>)> {
    sock.set_nonblocking(true)?;
    let sock = tokio::net::TcpStream::from_std(sock)?;
    let io = TokioTlsIo {
        conn,
        sock,
        pending_plaintext: Vec::new(),
        // Hyper's h2 state machine is not ALPS-aware, so keep the normal client
        // preface and SETTINGS even when TLS negotiated application settings.
        h2_alps_filter: H2AlpsWriteFilter::new(false),
    };
    let mut h2_builder = hyper::client::conn::http2::Builder::new(TokioExecutor);
    // Hyper's larger default flow-control windows make some fingerprinting endpoints close
    // after response headers. The RFC default window is more broadly tolerated.
    h2_builder
        .initial_stream_window_size(65_535)
        .initial_connection_window_size(65_535);
    let (mut sender, connection) = h2_builder.handshake(io).await?;
    let connection_task = tokio::task::spawn(async move { connection.await });

    let request = Request::builder()
        .method(Method::GET)
        .uri(hyper_uri(&uri)?)
        .header(ACCEPT_ENCODING, "identity")
        .header(USER_AGENT, "craftget/0.0.1")
        .body(EmptyBody)?;
    let response = sender.send_request(request).await?;
    let status = response.status().as_u16();
    let body_len = response
        .headers()
        .get(CONTENT_LENGTH)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.parse::<usize>().ok())
        .or_else(|| {
            response
                .body()
                .size_hint()
                .exact()
                .and_then(|len| usize::try_from(len).ok())
        });
    let body = read_http2_body(response.into_body(), body_read_mode, body_len).await?;
    connection_task.abort();

    Ok((status, body))
}

async fn read_http2_body(
    body: Incoming,
    body_read_mode: BodyReadMode,
    body_len: Option<usize>,
) -> Result<Vec<u8>> {
    let mut body = Box::pin(body);
    let mut output = Vec::new();
    while should_continue_http2_body_read(body_read_mode, body_len, output.len()) {
        let Some(frame) = poll_fn(|cx| body.as_mut().poll_frame(cx)).await else {
            break;
        };
        let frame = frame?;
        let Some(data) = frame.data_ref() else {
            continue;
        };
        let mode_remaining = body_read_mode
            .remaining(output.len())
            .unwrap_or(0);
        let remaining = body_len
            .and_then(|len| len.checked_sub(output.len()))
            .map(|len| len.min(mode_remaining))
            .unwrap_or(mode_remaining);
        output.extend_from_slice(&data[..data.len().min(remaining)]);
    }
    Ok(output)
}

fn should_continue_http2_body_read(
    body_read_mode: BodyReadMode,
    body_len: Option<usize>,
    current_len: usize,
) -> bool {
    body_read_mode.should_continue(current_len) && body_len.is_none_or(|len| current_len < len)
}

#[derive(Clone, Copy)]
enum BodyReadMode {
    Head { max_bytes: usize },
    Full,
}

fn body_read_mode(full_body: bool) -> BodyReadMode {
    match full_body {
        true => BodyReadMode::Full,
        false => BodyReadMode::Head {
            max_bytes: MAX_BODY_BYTES,
        },
    }
}

impl BodyReadMode {
    fn remaining(self, current_len: usize) -> Option<usize> {
        match self {
            Self::Full => Some(usize::MAX),
            Self::Head { max_bytes } => max_bytes.checked_sub(current_len),
        }
    }

    fn should_continue(self, current_len: usize) -> bool {
        self.remaining(current_len)
            .is_some_and(|remaining| remaining > 0)
    }
}

fn hyper_uri(uri: &Url) -> Result<Uri> {
    uri.as_str()
        .parse::<Uri>()
        .with_context(|| format!("failed to convert URL to hyper URI: {uri}"))
}

fn read_http1_response(
    reader: &mut impl BufRead,
    body_read_mode: BodyReadMode,
) -> Result<(u16, Vec<u8>)> {
    let mut status_line = String::new();
    if reader.read_line(&mut status_line)? == 0 {
        bail!("server closed before sending an HTTP response");
    }
    let status_code = status_line
        .split_whitespace()
        .nth(1)
        .ok_or_else(|| anyhow!("malformed HTTP status line: {status_line:?}"))?
        .parse::<u16>()
        .with_context(|| format!("malformed HTTP status line: {status_line:?}"))?;

    let mut headers = Vec::new();
    loop {
        let mut line = String::new();
        if reader.read_line(&mut line)? == 0 {
            bail!("server closed while sending HTTP headers");
        }
        if line == "\r\n" || line == "\n" {
            break;
        }
        if let Some((name, value)) = line.split_once(':') {
            headers.push((
                name.trim().to_ascii_lowercase(),
                value.trim().to_ascii_lowercase(),
            ));
        }
    }

    if headers.iter().any(|(name, value)| {
        name == "transfer-encoding"
            && value
                .split(',')
                .any(|part| part.trim() == "chunked")
    }) {
        read_chunked_body(reader, body_read_mode).map(|body| (status_code, body))
    } else if let Some(content_length) = headers
        .iter()
        .find(|(name, _)| name == "content-length")
        .and_then(|(_, value)| value.parse::<usize>().ok())
    {
        read_known_length_body(reader, content_length, body_read_mode)
            .map(|body| (status_code, body))
    } else {
        read_until_eof_body(reader, body_read_mode).map(|body| (status_code, body))
    }
}

fn read_chunked_body(reader: &mut impl BufRead, body_read_mode: BodyReadMode) -> Result<Vec<u8>> {
    let mut body = Vec::new();
    while body_read_mode.should_continue(body.len()) {
        let mut size_line = String::new();
        if reader.read_line(&mut size_line)? == 0 {
            bail!("server closed while sending a chunked body");
        }
        let size_hex = size_line
            .trim()
            .split_once(';')
            .map_or(size_line.trim(), |(size, _)| size.trim());
        let size = usize::from_str_radix(size_hex, 16)
            .with_context(|| format!("malformed chunk size: {size_line:?}"))?;
        if size == 0 {
            break;
        }

        let remaining = body_read_mode
            .remaining(body.len())
            .unwrap_or(0);
        let take = size.min(remaining);
        let mut chunk = vec![0u8; take];
        reader.read_exact(&mut chunk)?;
        body.extend_from_slice(&chunk);
        if take < size {
            break;
        }

        let mut crlf = [0u8; 2];
        reader.read_exact(&mut crlf)?;
    }
    Ok(body)
}

fn read_known_length_body(
    reader: &mut impl Read,
    content_length: usize,
    body_read_mode: BodyReadMode,
) -> Result<Vec<u8>> {
    let read_len = match body_read_mode {
        BodyReadMode::Full => content_length,
        BodyReadMode::Head { max_bytes } => content_length.min(max_bytes),
    };
    let mut body = vec![0u8; read_len];
    reader.read_exact(&mut body)?;
    Ok(body)
}

fn read_until_eof_body(reader: &mut impl Read, body_read_mode: BodyReadMode) -> Result<Vec<u8>> {
    let mut body = Vec::new();
    match body_read_mode {
        BodyReadMode::Full => {
            reader.read_to_end(&mut body)?;
        }
        BodyReadMode::Head { max_bytes } => {
            reader
                .take(max_bytes as u64)
                .read_to_end(&mut body)?;
        }
    }
    Ok(body)
}

struct EmptyBody;

impl Body for EmptyBody {
    type Data = Bytes;
    type Error = Infallible;

    fn poll_frame(
        self: Pin<&mut Self>,
        _cx: &mut TaskContext<'_>,
    ) -> Poll<Option<std::result::Result<Frame<Self::Data>, Self::Error>>> {
        Poll::Ready(None)
    }

    fn is_end_stream(&self) -> bool {
        true
    }

    fn size_hint(&self) -> SizeHint {
        SizeHint::with_exact(0)
    }
}

struct TokioTlsIo {
    conn: rustls::ClientConnection,
    sock: tokio::net::TcpStream,
    pending_plaintext: Vec<u8>,
    h2_alps_filter: H2AlpsWriteFilter,
}

impl TokioTlsIo {
    fn poll_drain_plaintext(&mut self, cx: &mut TaskContext<'_>) -> Poll<io::Result<()>> {
        while !self.pending_plaintext.is_empty() {
            let written = match self
                .conn
                .writer()
                .write(&self.pending_plaintext)
            {
                Ok(0) => {
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "failed to write HTTP/2 plaintext",
                    )));
                }
                Ok(written) => written,
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => {
                    match self.poll_flush_tls(cx) {
                        Poll::Ready(Ok(())) => continue,
                        Poll::Ready(Err(err)) => return Poll::Ready(Err(err)),
                        Poll::Pending => return Poll::Pending,
                    }
                }
                Err(err) => return Poll::Ready(Err(err)),
            };
            self.pending_plaintext.drain(..written);

            match self.poll_flush_tls(cx) {
                Poll::Ready(Ok(())) => {}
                Poll::Ready(Err(err)) => return Poll::Ready(Err(err)),
                Poll::Pending => return Poll::Pending,
            }
        }

        self.poll_flush_tls(cx)
    }

    fn poll_flush_tls(&mut self, cx: &mut TaskContext<'_>) -> Poll<io::Result<()>> {
        while self.conn.wants_write() {
            match self.sock.poll_write_ready(cx) {
                Poll::Ready(Ok(())) => {}
                Poll::Ready(Err(err)) => return Poll::Ready(Err(err)),
                Poll::Pending => return Poll::Pending,
            }

            let mut writer = TokioTcpWrite { sock: &self.sock };
            match self.conn.write_tls(&mut writer) {
                Ok(0) => {
                    return Poll::Ready(Err(io::Error::new(
                        io::ErrorKind::WriteZero,
                        "failed to write TLS data",
                    )));
                }
                Ok(_) => {}
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => continue,
                Err(err) => return Poll::Ready(Err(err)),
            }
        }

        Poll::Ready(Ok(()))
    }

    fn poll_read_tls(&mut self, cx: &mut TaskContext<'_>) -> Poll<io::Result<usize>> {
        loop {
            match self.sock.poll_read_ready(cx) {
                Poll::Ready(Ok(())) => {}
                Poll::Ready(Err(err)) => return Poll::Ready(Err(err)),
                Poll::Pending => return Poll::Pending,
            }

            let mut tls_record = [0u8; 16 * 1024];
            let read = match self.sock.try_read(&mut tls_record) {
                Ok(0) => return Poll::Ready(Ok(0)),
                Ok(read) => read,
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => continue,
                Err(err) => return Poll::Ready(Err(err)),
            };

            let mut record = &tls_record[..read];
            let mut consumed_total = 0;
            // rustls may intentionally consume only part of this slice per read_tls call.
            // Feed the whole TCP read into the deframer before returning to hyper.
            while !record.is_empty() {
                let consumed = self.conn.read_tls(&mut record)?;
                if consumed == 0 {
                    break;
                }
                consumed_total += consumed;
                self.conn
                    .process_new_packets()
                    .map_err(|err| io::Error::new(io::ErrorKind::InvalidData, err))?;
            }
            return Poll::Ready(Ok(consumed_total));
        }
    }
}

impl hyper::rt::Read for TokioTlsIo {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut TaskContext<'_>,
        mut buf: hyper::rt::ReadBufCursor<'_>,
    ) -> Poll<io::Result<()>> {
        let mut scratch = vec![0u8; buf.remaining().min(16 * 1024)];
        if scratch.is_empty() {
            return Poll::Ready(Ok(()));
        }

        let this = self.as_mut().get_mut();
        match this.poll_flush_tls(cx) {
            Poll::Ready(Ok(())) => {}
            Poll::Ready(Err(err)) => return Poll::Ready(Err(err)),
            Poll::Pending => return Poll::Pending,
        }

        loop {
            match this.conn.reader().read(&mut scratch) {
                Ok(0) => return Poll::Ready(Ok(())),
                Ok(read) => {
                    buf.put_slice(&scratch[..read]);
                    return Poll::Ready(Ok(()));
                }
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => {}
                Err(err) => return Poll::Ready(Err(err)),
            }

            match this.poll_read_tls(cx) {
                Poll::Ready(Ok(0)) => return Poll::Ready(Ok(())),
                Poll::Ready(Ok(_)) => {}
                Poll::Ready(Err(err)) => return Poll::Ready(Err(err)),
                Poll::Pending => return Poll::Pending,
            }
        }
    }
}

impl hyper::rt::Write for TokioTlsIo {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut TaskContext<'_>,
        buf: &[u8],
    ) -> Poll<io::Result<usize>> {
        let this = self.as_mut().get_mut();
        this.h2_alps_filter
            .filter(buf, &mut this.pending_plaintext);

        match this.poll_drain_plaintext(cx) {
            Poll::Ready(Ok(())) | Poll::Pending => Poll::Ready(Ok(buf.len())),
            Poll::Ready(Err(err)) => Poll::Ready(Err(err)),
        }
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut TaskContext<'_>) -> Poll<io::Result<()>> {
        let this = self.as_mut().get_mut();
        this.poll_drain_plaintext(cx)
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut TaskContext<'_>) -> Poll<io::Result<()>> {
        let this = self.as_mut().get_mut();
        this.conn.send_close_notify();
        this.poll_drain_plaintext(cx)
    }
}

enum H2AlpsWriteFilter {
    Disabled,
    Preface { matched: usize },
    FrameHeader { header: Vec<u8> },
    DropSettings { remaining: usize },
    Done,
}

impl H2AlpsWriteFilter {
    const PREFACE: &'static [u8] = b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n";

    fn new(enabled: bool) -> Self {
        match enabled {
            true => Self::Preface { matched: 0 },
            false => Self::Disabled,
        }
    }

    fn filter(&mut self, mut input: &[u8], output: &mut Vec<u8>) {
        loop {
            match self {
                Self::Disabled | Self::Done => {
                    output.extend_from_slice(input);
                    return;
                }
                Self::Preface { matched } => {
                    let need = Self::PREFACE.len() - *matched;
                    let take = need.min(input.len());
                    if input[..take] != Self::PREFACE[*matched..*matched + take] {
                        output.extend_from_slice(input);
                        *self = Self::Done;
                        return;
                    }

                    output.extend_from_slice(&input[..take]);
                    *matched += take;
                    input = &input[take..];
                    if *matched == Self::PREFACE.len() {
                        *self = Self::FrameHeader { header: Vec::new() };
                    }
                    if input.is_empty() {
                        return;
                    }
                }
                Self::FrameHeader { header } => {
                    let take = (9 - header.len()).min(input.len());
                    header.extend_from_slice(&input[..take]);
                    input = &input[take..];
                    if header.len() < 9 {
                        return;
                    }

                    let payload_len = ((usize::from(header[0])) << 16)
                        | ((usize::from(header[1])) << 8)
                        | usize::from(header[2]);
                    let frame_type = header[3];
                    let flags = header[4];
                    let stream_id =
                        u32::from_be_bytes([header[5], header[6], header[7], header[8]])
                            & 0x7fff_ffff;

                    if frame_type == 0x04 && flags & 0x01 == 0 && stream_id == 0 {
                        *self = Self::DropSettings {
                            remaining: payload_len,
                        };
                    } else {
                        output.extend_from_slice(header);
                        *self = Self::Done;
                    }
                    if input.is_empty() {
                        return;
                    }
                }
                Self::DropSettings { remaining } => {
                    let take = (*remaining).min(input.len());
                    *remaining -= take;
                    input = &input[take..];
                    if *remaining == 0 {
                        *self = Self::Done;
                    }
                    if input.is_empty() {
                        return;
                    }
                }
            }
        }
    }
}

struct TokioTcpWrite<'a> {
    sock: &'a tokio::net::TcpStream,
}

impl Write for TokioTcpWrite<'_> {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.sock.try_write(buf)
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

#[derive(Clone)]
struct TokioExecutor;

impl<F> hyper::rt::Executor<F> for TokioExecutor
where
    F: Future + Send + 'static,
    F::Output: Send + 'static,
{
    fn execute(&self, future: F) {
        tokio::task::spawn(future);
    }
}

fn fingerprint_builder(name: &str) -> Option<craft::FingerprintBuilder> {
    let normalized = name
        .to_ascii_lowercase()
        .replace('-', "_");
    match normalized.as_str() {
        "chrome" | "hellochrome_auto" | "chrome_112" | "hellochrome_112" => Some(
            craft::CHROME_112
                .test_alpn_http1
                .builder(),
        ),
        "chrome_108" | "hellochrome_108" => Some(
            craft::CHROME_108
                .test_alpn_http1
                .builder(),
        ),
        "chromium" | "chromium_144" | "hellochromium_144" => Some(craft::CHROMIUM_144.builder()),
        "chrome_148" | "chromium_148" | "hellochrome_148" | "hellochromium_148" => {
            Some(craft::CHROME_148.builder())
        }
        "craftlsmaxxing" | "craftmaxxing" | "hellocraftlsmaxxing" => {
            Some(craft::CRAFTLSMAXXING.builder())
        }
        "firefox" | "hellofirefox_auto" | "firefox_105" | "hellofirefox_105" => Some(
            craft::FIREFOX_105
                .test_alpn_http1
                .builder()
                .dangerous_disable_override_keyshare(),
        ),
        "firefox_140" | "hellofirefox_140" => Some(craft::FIREFOX_140.builder()),
        "safari" | "hellosafari_auto" | "safari_17_1" | "hellosafari_17_1" => Some(
            craft::SAFARI_17_1
                .test_alpn_http1
                .builder(),
        ),
        "rustls" | "craftls" | "rustls_test" => Some(
            craft::RUSTLS_TEST
                .test_alpn_http1
                .builder(),
        ),
        _ => None,
    }
}

fn default_connect_to(uri: &Url) -> Result<String> {
    let host = uri
        .host_str()
        .ok_or_else(|| anyhow!("URL has no host"))?;
    let port = uri.port().unwrap_or(default_port(uri)?);
    Ok(format_host_port(host, port))
}

fn default_tls_server_name(uri: &Url) -> Result<String> {
    uri.host_str()
        .map(str::to_owned)
        .ok_or_else(|| anyhow!("URL has no host"))
}

fn default_port(uri: &Url) -> Result<u16> {
    match uri.scheme() {
        "https" => Ok(443),
        "http" => Ok(80),
        scheme => bail!("can't determine port for scheme {scheme:?}"),
    }
}

fn format_host_port(host: &str, port: u16) -> String {
    if host.contains(':') && !host.starts_with('[') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    }
}

fn host_header(uri: &Url) -> Result<String> {
    let host = uri
        .host_str()
        .ok_or_else(|| anyhow!("URL has no host"))?;
    Ok(uri
        .port()
        .map_or_else(|| host.to_owned(), |port| format_host_port(host, port)))
}

fn request_target(uri: &Url) -> String {
    let mut target = if uri.path().is_empty() {
        "/".to_owned()
    } else {
        uri.path().to_owned()
    };
    if let Some(query) = uri.query() {
        target.push('?');
        target.push_str(query);
    }
    target
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn defaults_https_port() {
        let uri = Url::parse("https://example.com/path").unwrap();
        assert_eq!(default_connect_to(&uri).unwrap(), "example.com:443");
        assert_eq!(default_tls_server_name(&uri).unwrap(), "example.com");
    }

    #[test]
    fn defaults_explicit_port() {
        let uri = Url::parse("https://example.com:8443/path").unwrap();
        assert_eq!(default_connect_to(&uri).unwrap(), "example.com:8443");
        assert_eq!(host_header(&uri).unwrap(), "example.com:8443");
    }

    #[test]
    fn target_preserves_path_and_query() {
        let uri = Url::parse("https://example.com/a/b?q=1").unwrap();
        assert_eq!(request_target(&uri), "/a/b?q=1");
    }

    #[test]
    fn supports_uget_style_aliases() {
        assert!(fingerprint_builder("hellochrome_auto").is_some());
        assert!(fingerprint_builder("chromium_144").is_some());
        assert!(fingerprint_builder("chrome_148").is_some());
        assert!(fingerprint_builder("chromium_148").is_some());
        assert!(fingerprint_builder("craftlsmaxxing").is_some());
        assert!(fingerprint_builder("craftmaxxing").is_some());
        assert!(fingerprint_builder("hellofirefox_105").is_some());
        assert!(fingerprint_builder("firefox_140").is_some());
        assert!(fingerprint_builder("hellosafari_17_1").is_some());
        assert_eq!(
            fingerprint_builder("firefox_140")
                .unwrap()
                .ech_force_tls13(),
            Some(false)
        );
    }

    #[test]
    fn parses_sni_flags() {
        let args = Args::try_parse_from(["craftget", "https://example.com/"]).unwrap();
        assert!(args.send_sni);
        assert!(!args.no_sni);

        let args =
            Args::try_parse_from(["craftget", "--send-sni", "false", "https://example.com/"])
                .unwrap();
        assert!(!args.send_sni);
        assert!(!args.no_sni);

        let args = Args::try_parse_from(["craftget", "--no-sni", "https://example.com/"]).unwrap();
        assert!(args.send_sni);
        assert!(args.no_sni);
    }

    #[test]
    fn parses_ech_key_flags() {
        let args = Args::try_parse_from(["craftget", "--ech-key", "AAAA", "https://example.com/"])
            .unwrap();
        assert_eq!(args.ech_key.as_deref(), Some("AAAA"));
        assert!(args.ech_key_file.is_none());

        let args = Args::try_parse_from([
            "craftget",
            "--ech-config-file",
            "/tmp/example.ech",
            "https://example.com/",
        ])
        .unwrap();
        assert!(args.ech_key.is_none());
        assert_eq!(
            args.ech_key_file.as_deref(),
            Some(Path::new("/tmp/example.ech"))
        );

        let args = Args::try_parse_from([
            "craftget",
            "--ech-force-tls13",
            "false",
            "https://example.com/",
        ])
        .unwrap();
        assert_eq!(args.ech_force_tls13, Some(false));
    }

    #[test]
    fn parses_full_body_flag() {
        let args = Args::try_parse_from(["craftget", "https://example.com/"]).unwrap();
        assert!(!args.full_body);

        let args =
            Args::try_parse_from(["craftget", "--full-body", "https://example.com/"]).unwrap();
        assert!(args.full_body);
    }

    #[test]
    fn http2_body_read_respects_content_length() {
        assert!(should_continue_http2_body_read(
            BodyReadMode::Full,
            Some(11),
            10
        ));
        assert!(!should_continue_http2_body_read(
            BodyReadMode::Full,
            Some(11),
            11
        ));
        assert!(!should_continue_http2_body_read(
            BodyReadMode::Head { max_bytes: 5 },
            Some(11),
            5
        ));
    }

    #[test]
    fn decodes_ech_key_text() {
        let raw = include_bytes!("../../rustls/tests/data/localhost-echconfigs.bin");
        let encoded = BASE64.encode(raw);
        let decoded = decode_ech_key_text(&format!("ech=\"{encoded}\"")).unwrap();
        assert_eq!(decoded.as_ref(), raw);
    }

    #[test]
    fn rejects_multiple_ech_key_sources() {
        let err = load_ech_config_list(Some("AAAA"), Some(Path::new("/tmp/example.ech")))
            .unwrap_err()
            .to_string();
        assert!(err.contains("use only one"));
    }

    #[test]
    fn builds_config_with_supplied_ech_key() {
        let raw = include_bytes!("../../rustls/tests/data/localhost-echconfigs.bin");
        let config = build_client_config(
            fingerprint_builder("firefox_140").unwrap(),
            true,
            Some(EchConfigListBytes::from(raw.to_vec())),
            false,
        )
        .unwrap();
        assert!(config.enable_sni);
        assert!(
            !config
                .provider()
                .tls12_cipher_suites
                .is_empty()
        );
    }

    #[test]
    fn reads_content_length_response() {
        let mut response = b"HTTP/1.1 404 Not Found\r\nContent-Length: 5\r\n\r\nhello".as_slice();
        let (status, body) = read_http1_response(&mut response, body_read_mode(false)).unwrap();

        assert_eq!(status, 404);
        assert_eq!(body, b"hello");
    }

    #[test]
    fn reads_only_body_head_by_default() {
        let mut response = b"HTTP/1.1 200 OK\r\nContent-Length: 11\r\n\r\nhello world".as_slice();
        let (status, body) =
            read_http1_response(&mut response, BodyReadMode::Head { max_bytes: 5 }).unwrap();

        assert_eq!(status, 200);
        assert_eq!(body, b"hello");
    }

    #[test]
    fn reads_full_content_length_response_when_requested() {
        let mut response = b"HTTP/1.1 200 OK\r\nContent-Length: 11\r\n\r\nhello world".as_slice();
        let (status, body) = read_http1_response(&mut response, BodyReadMode::Full).unwrap();

        assert_eq!(status, 200);
        assert_eq!(body, b"hello world");
    }

    #[test]
    fn reads_chunked_response_with_actual_status() {
        let mut response =
            b"HTTP/1.1 206 Partial Content\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n"
                .as_slice();
        let (status, body) = read_http1_response(&mut response, body_read_mode(false)).unwrap();

        assert_eq!(status, 206);
        assert_eq!(body, b"hello");
    }

    #[test]
    fn reads_full_chunked_response_when_requested() {
        let mut response =
            b"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n6\r\n world\r\n0\r\n\r\n"
                .as_slice();
        let (status, body) = read_http1_response(&mut response, BodyReadMode::Full).unwrap();

        assert_eq!(status, 200);
        assert_eq!(body, b"hello world");
    }

    #[test]
    fn h2_alps_filter_passes_through_when_disabled() {
        let mut filter = H2AlpsWriteFilter::new(false);
        let input = b"hello";
        let mut output = Vec::new();

        filter.filter(input, &mut output);

        assert_eq!(output, input);
    }

    #[test]
    fn h2_alps_filter_omits_initial_settings_after_preface() {
        let mut filter = H2AlpsWriteFilter::new(true);
        let mut input = H2AlpsWriteFilter::PREFACE.to_vec();
        input.extend_from_slice(&[
            0x00, 0x00, 0x06, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, // SETTINGS
            0x00, 0x04, 0x00, 0x20, 0x00, 0x00, // INITIAL_WINDOW_SIZE
            0x00, 0x00, 0x00, 0x01, 0x05, 0x00, 0x00, 0x00, 0x01, // HEADERS
        ]);
        let mut output = Vec::new();

        filter.filter(&input, &mut output);

        let mut expected = H2AlpsWriteFilter::PREFACE.to_vec();
        expected.extend_from_slice(&[0x00, 0x00, 0x00, 0x01, 0x05, 0x00, 0x00, 0x00, 0x01]);
        assert_eq!(output, expected);
    }

    #[test]
    fn h2_alps_filter_handles_split_initial_settings() {
        let mut filter = H2AlpsWriteFilter::new(true);
        let mut input = H2AlpsWriteFilter::PREFACE.to_vec();
        input.extend_from_slice(&[
            0x00, 0x00, 0x06, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, // SETTINGS
            0x00, 0x04, 0x00, 0x20, 0x00, 0x00, // INITIAL_WINDOW_SIZE
            0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, // WINDOW_UPDATE
        ]);
        let mut output = Vec::new();

        filter.filter(&input[..28], &mut output);
        filter.filter(&input[28..34], &mut output);
        filter.filter(&input[34..], &mut output);

        let mut expected = H2AlpsWriteFilter::PREFACE.to_vec();
        expected.extend_from_slice(&[0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00]);
        assert_eq!(output, expected);
    }
}
