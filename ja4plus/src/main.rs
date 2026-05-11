use core::time::Duration;
use std::io::Read;
use std::net::{TcpListener, TcpStream};

use anyhow::{Context, Result, anyhow, bail};
use clap::Parser;
use ring::digest::{SHA256, digest};
use serde::Serialize;

const DEFAULT_LISTEN: &str = "127.0.0.1:8443";
const READ_TIMEOUT: Duration = Duration::from_secs(10);
const MAX_CLIENT_HELLO_BYTES: usize = 256 * 1024;
const ZERO_HASH: &str = "000000000000";

#[derive(Parser)]
#[command(about = "Print JA4+ ClientHello fingerprint info for incoming TCP TLS connections")]
struct Args {
    /// TCP address to listen on.
    #[arg(long, default_value = DEFAULT_LISTEN)]
    listen: String,

    /// Exit after one accepted connection.
    #[arg(long)]
    once: bool,

    /// Maximum accepted ClientHello handshake size in bytes.
    #[arg(long, default_value_t = MAX_CLIENT_HELLO_BYTES)]
    max_client_hello_bytes: usize,

    /// Include a reversible structured ClientHello dump in each JSON line.
    #[arg(long, alias = "structured-client-hello")]
    structured_dump: bool,
}

#[derive(Debug, Clone, Default)]
struct ClientHello {
    legacy_version: u16,
    server_name: Option<String>,
    cipher_suites: Vec<u16>,
    extensions: Vec<u16>,
    supported_versions: Vec<u16>,
    signature_schemes: Vec<u16>,
    alpn_protocols: Vec<Vec<u8>>,
    handshake_len: usize,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct StructuredClientHello {
    legacy_version: u16,
    random: [u8; 32],
    session_id: Vec<u8>,
    cipher_suites: Vec<u16>,
    compression_methods: Vec<u8>,
    extensions_present: bool,
    extensions: Vec<StructuredClientHelloExtension>,
    handshake_len: usize,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct StructuredClientHelloExtension {
    extension_type: u16,
    payload: Vec<u8>,
}

#[derive(Serialize)]
struct Output {
    remote_addr: String,
    local_addr: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    ja4: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    client_hello: Option<ClientHelloOutput>,
    #[serde(skip_serializing_if = "Option::is_none")]
    client_hello_structured: Option<StructuredClientHelloOutput>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

#[derive(Serialize)]
struct ClientHelloOutput {
    legacy_version: String,
    server_name: Option<String>,
    alpn_protocols: Vec<String>,
    supported_versions: Vec<String>,
    cipher_suites: Vec<String>,
    extensions: Vec<String>,
    signature_schemes: Vec<String>,
    handshake_bytes: usize,
}

#[derive(Serialize)]
struct StructuredClientHelloOutput {
    handshake_type: String,
    legacy_version: String,
    random_hex: String,
    session_id_hex: String,
    cipher_suites: Vec<String>,
    compression_methods: Vec<String>,
    extensions_present: bool,
    extensions: Vec<StructuredClientHelloExtensionOutput>,
    handshake_bytes: usize,
}

#[derive(Serialize)]
struct StructuredClientHelloExtensionOutput {
    #[serde(rename = "type")]
    extension_type: String,
    payload_hex: String,
}

fn main() -> Result<()> {
    let args = Args::parse();
    let listener = TcpListener::bind(&args.listen)
        .with_context(|| format!("failed to bind {}", args.listen))?;
    eprintln!("ja4plus listening on {}", listener.local_addr()?);

    for stream in listener.incoming() {
        let mut stream = match stream {
            Ok(stream) => stream,
            Err(err) => {
                eprintln!("accept failed: {err}");
                continue;
            }
        };

        let output = handle_connection(
            &mut stream,
            args.max_client_hello_bytes,
            args.structured_dump,
        );
        serde_json::to_writer(std::io::stdout().lock(), &output)?;
        println!();

        if args.once {
            break;
        }
    }

    Ok(())
}

fn handle_connection(
    stream: &mut TcpStream,
    max_client_hello_bytes: usize,
    structured_dump: bool,
) -> Output {
    let remote_addr = stream
        .peer_addr()
        .map(|addr| addr.to_string())
        .unwrap_or_default();
    let local_addr = stream
        .local_addr()
        .map(|addr| addr.to_string())
        .unwrap_or_default();
    let _ = stream.set_read_timeout(Some(READ_TIMEOUT));

    match read_client_hello(stream, max_client_hello_bytes).and_then(|handshake| {
        let hello = parse_client_hello_handshake(&handshake)?;
        let structured = if structured_dump {
            Some(parse_structured_client_hello_handshake(&handshake)?)
        } else {
            None
        };
        Ok((hello, structured))
    }) {
        Ok((hello, structured)) => Output {
            remote_addr,
            local_addr,
            ja4: Some(ja4(&hello)),
            client_hello: Some(ClientHelloOutput::from(&hello)),
            client_hello_structured: structured
                .as_ref()
                .map(StructuredClientHelloOutput::from),
            error: None,
        },
        Err(err) => Output {
            remote_addr,
            local_addr,
            ja4: None,
            client_hello: None,
            client_hello_structured: None,
            error: Some(err.to_string()),
        },
    }
}

fn read_client_hello(stream: &mut impl Read, max_client_hello_bytes: usize) -> Result<Vec<u8>> {
    let mut first_header = [0u8; 5];
    stream
        .read_exact(&mut first_header)
        .context("failed to read first TLS record header")?;
    let mut payload = read_handshake_record(stream, first_header)?;
    if payload.len() < 4 {
        bail!("TLS handshake record is too short");
    }
    if payload[0] != 1 {
        bail!("first handshake message is not ClientHello: {}", payload[0]);
    }

    let handshake_len = take_u24_at(&payload, 1)? + 4;
    if handshake_len > max_client_hello_bytes {
        bail!("ClientHello exceeds configured size limit: {handshake_len}");
    }

    while payload.len() < handshake_len {
        let mut header = [0u8; 5];
        stream
            .read_exact(&mut header)
            .context("failed to read continued TLS record header")?;
        payload.extend(read_handshake_record(stream, header)?);
        if payload.len() > max_client_hello_bytes {
            bail!("ClientHello exceeds configured size limit");
        }
    }

    payload.truncate(handshake_len);
    Ok(payload)
}

fn read_handshake_record(stream: &mut impl Read, header: [u8; 5]) -> Result<Vec<u8>> {
    if header[0] != 22 {
        bail!("TLS record is not a handshake record: {}", header[0]);
    }
    let len = u16::from_be_bytes([header[3], header[4]]) as usize;
    let mut payload = vec![0u8; len];
    stream
        .read_exact(&mut payload)
        .context("failed to read TLS handshake record payload")?;
    Ok(payload)
}

fn parse_client_hello_handshake(handshake: &[u8]) -> Result<ClientHello> {
    let mut offset = 0;
    let typ = take_u8(handshake, &mut offset)?;
    if typ != 1 {
        bail!("handshake message is not ClientHello: {typ}");
    }
    let body_len = take_u24(handshake, &mut offset)?;
    if handshake.len() != offset + body_len {
        bail!(
            "ClientHello length mismatch: header says {}, record has {}",
            body_len,
            handshake.len().saturating_sub(offset)
        );
    }

    let body = take(handshake, &mut offset, body_len)?;
    let mut hello = parse_client_hello_body(body)?;
    hello.handshake_len = handshake.len();
    Ok(hello)
}

fn parse_structured_client_hello_handshake(handshake: &[u8]) -> Result<StructuredClientHello> {
    let mut offset = 0;
    let typ = take_u8(handshake, &mut offset)?;
    if typ != 1 {
        bail!("handshake message is not ClientHello: {typ}");
    }
    let body_len = take_u24(handshake, &mut offset)?;
    if handshake.len() != offset + body_len {
        bail!(
            "ClientHello length mismatch: header says {}, record has {}",
            body_len,
            handshake.len().saturating_sub(offset)
        );
    }

    let body = take(handshake, &mut offset, body_len)?;
    let mut body_offset = 0;
    let legacy_version = take_u16(body, &mut body_offset)?;
    let mut random = [0u8; 32];
    random.copy_from_slice(take(body, &mut body_offset, 32)?);

    let session_id_len = take_u8(body, &mut body_offset)? as usize;
    let session_id = take(body, &mut body_offset, session_id_len)?.to_vec();

    let cipher_suites_len = take_u16(body, &mut body_offset)? as usize;
    if cipher_suites_len % 2 != 0 {
        bail!("cipher_suites vector has an odd length");
    }
    let cipher_suites = take(body, &mut body_offset, cipher_suites_len)?
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect();

    let compression_methods_len = take_u8(body, &mut body_offset)? as usize;
    let compression_methods = take(body, &mut body_offset, compression_methods_len)?.to_vec();

    let mut extensions_present = false;
    let mut extensions = Vec::new();
    if body_offset != body.len() {
        extensions_present = true;
        let extensions_len = take_u16(body, &mut body_offset)? as usize;
        let extensions_end = body_offset
            .checked_add(extensions_len)
            .ok_or_else(|| anyhow!("extension length overflow"))?;
        if extensions_end != body.len() {
            bail!("ClientHello extension block length mismatch");
        }

        while body_offset < extensions_end {
            let extension_type = take_u16(body, &mut body_offset)?;
            let extension_len = take_u16(body, &mut body_offset)? as usize;
            let payload = take(body, &mut body_offset, extension_len)?.to_vec();
            extensions.push(StructuredClientHelloExtension {
                extension_type,
                payload,
            });
        }
    }

    Ok(StructuredClientHello {
        legacy_version,
        random,
        session_id,
        cipher_suites,
        compression_methods,
        extensions_present,
        extensions,
        handshake_len: handshake.len(),
    })
}

fn parse_client_hello_body(body: &[u8]) -> Result<ClientHello> {
    let mut offset = 0;
    let legacy_version = take_u16(body, &mut offset)?;
    take(body, &mut offset, 32)?;

    let session_id_len = take_u8(body, &mut offset)? as usize;
    take(body, &mut offset, session_id_len)?;

    let cipher_suites_len = take_u16(body, &mut offset)? as usize;
    if cipher_suites_len % 2 != 0 {
        bail!("cipher_suites vector has an odd length");
    }
    let cipher_suites_bytes = take(body, &mut offset, cipher_suites_len)?;
    let cipher_suites = cipher_suites_bytes
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect::<Vec<_>>();

    let compression_methods_len = take_u8(body, &mut offset)? as usize;
    take(body, &mut offset, compression_methods_len)?;

    let mut hello = ClientHello {
        legacy_version,
        cipher_suites,
        ..ClientHello::default()
    };

    if offset == body.len() {
        hello
            .supported_versions
            .push(legacy_version);
        return Ok(hello);
    }

    let extensions_len = take_u16(body, &mut offset)? as usize;
    let extensions_end = offset
        .checked_add(extensions_len)
        .ok_or_else(|| anyhow!("extension length overflow"))?;
    if extensions_end != body.len() {
        bail!("ClientHello extension block length mismatch");
    }

    while offset < extensions_end {
        let extension_type = take_u16(body, &mut offset)?;
        let extension_len = take_u16(body, &mut offset)? as usize;
        let extension = take(body, &mut offset, extension_len)?;
        hello.extensions.push(extension_type);

        match extension_type {
            0x0000 => hello.server_name = parse_sni(extension),
            0x000d => hello.signature_schemes = parse_u16_vector(extension, 2).unwrap_or_default(),
            0x0010 => hello.alpn_protocols = parse_alpn(extension).unwrap_or_default(),
            0x002b => {
                hello.supported_versions = parse_supported_versions(extension).unwrap_or_default();
            }
            _ => {}
        }
    }

    if hello.supported_versions.is_empty() {
        hello
            .supported_versions
            .push(legacy_version);
    }

    Ok(hello)
}

fn parse_sni(extension: &[u8]) -> Option<String> {
    let mut offset = 0;
    let list_len = take_u16(extension, &mut offset).ok()? as usize;
    let list_end = offset.checked_add(list_len)?;
    if list_end > extension.len() {
        return None;
    }
    while offset < list_end {
        let name_type = take_u8(extension, &mut offset).ok()?;
        let name_len = take_u16(extension, &mut offset).ok()? as usize;
        let name = take(extension, &mut offset, name_len).ok()?;
        if name_type == 0 {
            return Some(String::from_utf8_lossy(name).into_owned());
        }
    }
    None
}

fn parse_alpn(extension: &[u8]) -> Result<Vec<Vec<u8>>> {
    let mut offset = 0;
    let list_len = take_u16(extension, &mut offset)? as usize;
    let list_end = offset
        .checked_add(list_len)
        .ok_or_else(|| anyhow!("ALPN list length overflow"))?;
    if list_end > extension.len() {
        bail!("ALPN list length exceeds extension length");
    }

    let mut protocols = Vec::new();
    while offset < list_end {
        let protocol_len = take_u8(extension, &mut offset)? as usize;
        protocols.push(take(extension, &mut offset, protocol_len)?.to_vec());
    }
    Ok(protocols)
}

fn parse_supported_versions(extension: &[u8]) -> Result<Vec<u16>> {
    let mut offset = 0;
    let versions_len = take_u8(extension, &mut offset)? as usize;
    if versions_len % 2 != 0 {
        bail!("supported_versions vector has an odd length");
    }
    let versions = take(extension, &mut offset, versions_len)?;
    Ok(versions
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect())
}

fn parse_u16_vector(extension: &[u8], prefix_len: usize) -> Result<Vec<u16>> {
    let mut offset = 0;
    let item_bytes_len = match prefix_len {
        1 => take_u8(extension, &mut offset)? as usize,
        2 => take_u16(extension, &mut offset)? as usize,
        _ => bail!("unsupported vector prefix length {prefix_len}"),
    };
    if item_bytes_len % 2 != 0 {
        bail!("u16 vector has an odd length");
    }
    let item_bytes = take(extension, &mut offset, item_bytes_len)?;
    Ok(item_bytes
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect())
}

fn ja4(hello: &ClientHello) -> String {
    let version = ja4_version(hello);
    let sni = if hello.server_name.is_some() {
        'd'
    } else {
        'i'
    };
    let cipher_count = hello
        .cipher_suites
        .iter()
        .filter(|suite| !is_grease(**suite))
        .count()
        .min(99);
    let extension_count = hello
        .extensions
        .iter()
        .filter(|extension| !is_grease(**extension))
        .count()
        .min(99);
    let alpn = ja4_alpn(hello);
    let ciphers_hash = cipher_suite_hash(&hello.cipher_suites);
    let extensions_hash = extension_hash(&hello.extensions, &hello.signature_schemes);

    format!(
        "t{version}{sni}{cipher_count:02}{extension_count:02}{alpn}_{ciphers_hash}_{extensions_hash}"
    )
}

fn ja4_version(hello: &ClientHello) -> &'static str {
    let max_version = hello
        .supported_versions
        .iter()
        .copied()
        .filter(|version| !is_grease(*version))
        .max();

    match max_version {
        Some(0x0301) => "10",
        Some(0x0302) => "11",
        Some(0x0303) => "12",
        Some(0x0304) => "13",
        Some(0x0300) => "s3",
        Some(0x0002) => "s2",
        Some(0xfeff) => "d1",
        Some(0xfefd) => "d2",
        Some(0xfefc) => "d3",
        _ => "00",
    }
}

fn ja4_alpn(hello: &ClientHello) -> String {
    for protocol in &hello.alpn_protocols {
        if protocol.len() < 2 {
            continue;
        }
        let first_two = u16::from_be_bytes([protocol[0], protocol[1]]);
        if is_grease(first_two) {
            continue;
        }
        return String::from_utf8_lossy(&[protocol[0], *protocol.last().unwrap()]).into_owned();
    }
    "00".to_owned()
}

fn cipher_suite_hash(cipher_suites: &[u16]) -> String {
    let mut filtered = cipher_suites
        .iter()
        .copied()
        .filter(|suite| !is_grease(*suite))
        .collect::<Vec<_>>();
    if filtered.is_empty() {
        return ZERO_HASH.to_owned();
    }
    filtered.sort_unstable();
    truncated_hash_hex(join_hex_u16(&filtered).as_bytes())
}

fn extension_hash(extensions: &[u16], signature_schemes: &[u16]) -> String {
    let mut filtered = extensions
        .iter()
        .copied()
        .filter(|extension| !is_grease(*extension))
        .collect::<Vec<_>>();
    filtered.sort_unstable();

    let mut list = Vec::new();
    for extension in filtered {
        if matches!(extension, 0x0000 | 0x0010) {
            continue;
        }
        list.push(extension);
    }
    if list.is_empty() {
        return ZERO_HASH.to_owned();
    }

    let mut input = join_hex_u16(&list);
    let mut has_signature = false;
    for signature in signature_schemes {
        if is_grease(*signature) {
            continue;
        }
        if !has_signature {
            input.push('_');
            has_signature = true;
        } else {
            input.push(',');
        }
        input.push_str(&hex_u16(*signature));
    }

    truncated_hash_hex(input.as_bytes())
}

fn truncated_hash_hex(input: &[u8]) -> String {
    let hash = digest(&SHA256, input);
    hash.as_ref()[..6]
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn join_hex_u16(values: &[u16]) -> String {
    values
        .iter()
        .map(|value| hex_u16(*value))
        .collect::<Vec<_>>()
        .join(",")
}

fn hex_u16(value: u16) -> String {
    format!("{value:04x}")
}

fn is_grease(value: u16) -> bool {
    value & 0x000f == 0x000a && value >> 8 == value & 0x00ff
}

fn take<'a>(bytes: &'a [u8], offset: &mut usize, len: usize) -> Result<&'a [u8]> {
    let end = offset
        .checked_add(len)
        .ok_or_else(|| anyhow!("offset overflow"))?;
    if end > bytes.len() {
        bail!("unexpected end of ClientHello");
    }
    let taken = &bytes[*offset..end];
    *offset = end;
    Ok(taken)
}

fn take_u8(bytes: &[u8], offset: &mut usize) -> Result<u8> {
    Ok(take(bytes, offset, 1)?[0])
}

fn take_u16(bytes: &[u8], offset: &mut usize) -> Result<u16> {
    let bytes = take(bytes, offset, 2)?;
    Ok(u16::from_be_bytes([bytes[0], bytes[1]]))
}

fn take_u24(bytes: &[u8], offset: &mut usize) -> Result<usize> {
    let bytes = take(bytes, offset, 3)?;
    Ok(((bytes[0] as usize) << 16) | ((bytes[1] as usize) << 8) | bytes[2] as usize)
}

fn take_u24_at(bytes: &[u8], offset: usize) -> Result<usize> {
    if offset + 3 > bytes.len() {
        bail!("unexpected end while reading uint24");
    }
    Ok(((bytes[offset] as usize) << 16)
        | ((bytes[offset + 1] as usize) << 8)
        | bytes[offset + 2] as usize)
}

impl From<&ClientHello> for ClientHelloOutput {
    fn from(hello: &ClientHello) -> Self {
        Self {
            legacy_version: format!("0x{:04x}", hello.legacy_version),
            server_name: hello.server_name.clone(),
            alpn_protocols: hello
                .alpn_protocols
                .iter()
                .map(|protocol| String::from_utf8_lossy(protocol).into_owned())
                .collect(),
            supported_versions: format_u16_list(&hello.supported_versions),
            cipher_suites: format_u16_list(&hello.cipher_suites),
            extensions: format_u16_list(&hello.extensions),
            signature_schemes: format_u16_list(&hello.signature_schemes),
            handshake_bytes: hello.handshake_len,
        }
    }
}

impl From<&StructuredClientHello> for StructuredClientHelloOutput {
    fn from(hello: &StructuredClientHello) -> Self {
        Self {
            handshake_type: "0x01".to_owned(),
            legacy_version: format!("0x{:04x}", hello.legacy_version),
            random_hex: hex_bytes(&hello.random),
            session_id_hex: hex_bytes(&hello.session_id),
            cipher_suites: format_u16_list(&hello.cipher_suites),
            compression_methods: format_u8_list(&hello.compression_methods),
            extensions_present: hello.extensions_present,
            extensions: hello
                .extensions
                .iter()
                .map(StructuredClientHelloExtensionOutput::from)
                .collect(),
            handshake_bytes: hello.handshake_len,
        }
    }
}

impl From<&StructuredClientHelloExtension> for StructuredClientHelloExtensionOutput {
    fn from(extension: &StructuredClientHelloExtension) -> Self {
        Self {
            extension_type: format!("0x{:04x}", extension.extension_type),
            payload_hex: hex_bytes(&extension.payload),
        }
    }
}

#[cfg(test)]
impl StructuredClientHello {
    fn to_handshake_bytes(&self) -> Result<Vec<u8>> {
        let mut body = Vec::new();
        body.extend_from_slice(&self.legacy_version.to_be_bytes());
        body.extend_from_slice(&self.random);

        let session_id_len = checked_u8_len("session_id", self.session_id.len())?;
        body.push(session_id_len);
        body.extend_from_slice(&self.session_id);

        let cipher_suites_bytes_len = self
            .cipher_suites
            .len()
            .checked_mul(2)
            .ok_or_else(|| anyhow!("cipher_suites length overflow"))?;
        let cipher_suites_bytes_len = checked_u16_len("cipher_suites", cipher_suites_bytes_len)?;
        body.extend_from_slice(&cipher_suites_bytes_len.to_be_bytes());
        for suite in &self.cipher_suites {
            body.extend_from_slice(&suite.to_be_bytes());
        }

        let compression_methods_len =
            checked_u8_len("compression_methods", self.compression_methods.len())?;
        body.push(compression_methods_len);
        body.extend_from_slice(&self.compression_methods);

        if self.extensions_present {
            let mut extensions = Vec::new();
            for extension in &self.extensions {
                extensions.extend_from_slice(&extension.extension_type.to_be_bytes());
                let payload_len = checked_u16_len("extension payload", extension.payload.len())?;
                extensions.extend_from_slice(&payload_len.to_be_bytes());
                extensions.extend_from_slice(&extension.payload);
            }

            let extensions_len = checked_u16_len("extensions", extensions.len())?;
            body.extend_from_slice(&extensions_len.to_be_bytes());
            body.extend_from_slice(&extensions);
        }

        let body_len = checked_u24_len("ClientHello body", body.len())?;
        let mut handshake = Vec::with_capacity(4 + body.len());
        handshake.push(1);
        handshake.push(((body_len >> 16) & 0xff) as u8);
        handshake.push(((body_len >> 8) & 0xff) as u8);
        handshake.push((body_len & 0xff) as u8);
        handshake.extend_from_slice(&body);
        Ok(handshake)
    }
}

#[cfg(test)]
fn checked_u8_len(name: &str, len: usize) -> Result<u8> {
    u8::try_from(len).with_context(|| format!("{name} length exceeds u8: {len}"))
}

#[cfg(test)]
fn checked_u16_len(name: &str, len: usize) -> Result<u16> {
    u16::try_from(len).with_context(|| format!("{name} length exceeds u16: {len}"))
}

#[cfg(test)]
fn checked_u24_len(name: &str, len: usize) -> Result<usize> {
    if len > 0x00ff_ffff {
        bail!("{name} length exceeds u24: {len}");
    }
    Ok(len)
}

fn format_u16_list(values: &[u16]) -> Vec<String> {
    values
        .iter()
        .map(|value| format!("0x{value:04x}"))
        .collect()
}

fn format_u8_list(values: &[u8]) -> Vec<String> {
    values
        .iter()
        .map(|value| format!("0x{value:02x}"))
        .collect()
}

fn hex_bytes(bytes: &[u8]) -> String {
    bytes
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ja4_matches_reference_vectors() {
        let cases = [
            (
                ClientHello {
                    supported_versions: vec![0x0304],
                    alpn_protocols: vec![b"http/1.1".to_vec()],
                    ..ClientHello::default()
                },
                "t13i0000h1_000000000000_000000000000",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x0303, 0x0304],
                    alpn_protocols: vec![b"h2".to_vec(), b"http/1.1".to_vec()],
                    ..ClientHello::default()
                },
                "t13i0000h2_000000000000_000000000000",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x0304],
                    alpn_protocols: vec![b"http/1.1".to_vec()],
                    server_name: Some("example.com".to_owned()),
                    ..ClientHello::default()
                },
                "t13d0000h1_000000000000_000000000000",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x0304],
                    cipher_suites: vec![0x1301, 0x1302],
                    alpn_protocols: vec![b"http/1.1".to_vec()],
                    ..ClientHello::default()
                },
                "t13i0200h1_62ed6f6ca7ad_000000000000",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x0304],
                    alpn_protocols: vec![b"http/1.1".to_vec()],
                    extensions: vec![0x0000, 0x0010, 0x1a1a],
                    ..ClientHello::default()
                },
                "t13i0002h1_000000000000_000000000000",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x0304],
                    alpn_protocols: vec![b"http/1.1".to_vec()],
                    extensions: vec![0x0000, 0x1a1a, 0x0042],
                    signature_schemes: vec![0x0401, 0x0403],
                    ..ClientHello::default()
                },
                "t13i0002h1_000000000000_5b56ea7744b1",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x0304, 0x1a1a, 0x0303, 0x2a2a],
                    alpn_protocols: vec![b"http/1.1".to_vec()],
                    ..ClientHello::default()
                },
                "t13i0000h1_000000000000_000000000000",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x1a1a, 0x0304],
                    alpn_protocols: vec![vec![0x1a, 0x1a], b"http/1.1".to_vec()],
                    cipher_suites: vec![0x1301, 0x1302],
                    extensions: vec![0x0000, 0x1a1a, 0x0042],
                    signature_schemes: vec![0x1a1a, 0x0401, 0x0403],
                    ..ClientHello::default()
                },
                "t13i0202h1_62ed6f6ca7ad_5b56ea7744b1",
            ),
            (
                ClientHello {
                    supported_versions: vec![0x0304],
                    alpn_protocols: vec![b"a".to_vec()],
                    ..ClientHello::default()
                },
                "t13i000000_000000000000_000000000000",
            ),
        ];

        for (hello, expected) in cases {
            assert_eq!(ja4(&hello), expected);
        }
    }

    #[test]
    fn parses_client_hello_record() {
        let handshake = sample_client_hello_handshake();
        let hello = parse_client_hello_handshake(&handshake).unwrap();

        assert_eq!(hello.server_name.as_deref(), Some("example.com"));
        assert_eq!(hello.supported_versions, vec![0x0304, 0x0303]);
        assert_eq!(hello.cipher_suites, vec![0x1301, 0x1302]);
        assert_eq!(hello.alpn_protocols, vec![b"http/1.1".to_vec()]);
        assert_eq!(hello.signature_schemes, vec![0x0401, 0x0403]);
        assert_eq!(ja4(&hello), "t13d0204h1_62ed6f6ca7ad_fbe3ddfdc31f");
    }

    #[test]
    fn structured_dump_round_trips_client_hello_handshake() {
        let handshake = sample_client_hello_handshake();
        let structured = parse_structured_client_hello_handshake(&handshake).unwrap();

        assert_eq!(structured.to_handshake_bytes().unwrap(), handshake);
        assert_eq!(structured.legacy_version, 0x0303);
        assert_eq!(structured.random, [0u8; 32]);
        assert_eq!(structured.session_id, Vec::<u8>::new());
        assert_eq!(structured.cipher_suites, vec![0x1301, 0x1302]);
        assert_eq!(structured.compression_methods, vec![0]);
        assert!(structured.extensions_present);
        assert_eq!(structured.extensions.len(), 4);
        assert_eq!(structured.extensions[0].extension_type, 0x0000);
        assert_eq!(
            structured.extensions[0].payload,
            sni_extension("example.com")
        );

        let output = StructuredClientHelloOutput::from(&structured);
        assert_eq!(output.handshake_type, "0x01");
        assert_eq!(output.legacy_version, "0x0303");
        assert_eq!(output.random_hex, "00".repeat(32));
        assert_eq!(output.session_id_hex, "");
        assert_eq!(output.cipher_suites, vec!["0x1301", "0x1302"]);
        assert_eq!(output.compression_methods, vec!["0x00"]);
        assert!(output.extensions_present);
        assert_eq!(output.extensions[0].extension_type, "0x0000");
        assert_eq!(
            output.extensions[0].payload_hex,
            hex_bytes(&sni_extension("example.com"))
        );
        assert_eq!(output.handshake_bytes, handshake.len());
    }

    #[test]
    fn structured_dump_preserves_extension_block_presence() {
        let without_extensions = minimal_client_hello_handshake(false);
        let structured = parse_structured_client_hello_handshake(&without_extensions).unwrap();
        assert!(!structured.extensions_present);
        assert_eq!(structured.to_handshake_bytes().unwrap(), without_extensions);

        let with_empty_extensions = minimal_client_hello_handshake(true);
        let structured = parse_structured_client_hello_handshake(&with_empty_extensions).unwrap();
        assert!(structured.extensions_present);
        assert!(structured.extensions.is_empty());
        assert_eq!(
            structured.to_handshake_bytes().unwrap(),
            with_empty_extensions
        );
    }

    #[test]
    fn parses_structured_dump_flag() {
        let args = Args::try_parse_from(["ja4plus", "--structured-dump"]).unwrap();
        assert!(args.structured_dump);

        let args = Args::try_parse_from(["ja4plus", "--structured-client-hello"]).unwrap();
        assert!(args.structured_dump);
    }

    #[test]
    fn grease_filter_matches_reference_values() {
        for value in [
            0x0a0a, 0x1a1a, 0x2a2a, 0x3a3a, 0x4a4a, 0x5a5a, 0x6a6a, 0x7a7a, 0x8a8a, 0x9a9a, 0xaaaa,
            0xbaba, 0xcaca, 0xdada, 0xeaea, 0xfafa,
        ] {
            assert!(is_grease(value));
        }
        assert!(!is_grease(0x0304));
        assert!(!is_grease(0x1301));
    }

    fn sample_client_hello_handshake() -> Vec<u8> {
        let mut body = Vec::new();
        body.extend_from_slice(&0x0303u16.to_be_bytes());
        body.extend_from_slice(&[0u8; 32]);
        body.push(0);
        body.extend_from_slice(&4u16.to_be_bytes());
        body.extend_from_slice(&0x1301u16.to_be_bytes());
        body.extend_from_slice(&0x1302u16.to_be_bytes());
        body.push(1);
        body.push(0);

        let mut extensions = Vec::new();
        push_extension(&mut extensions, 0x0000, &sni_extension("example.com"));
        push_extension(&mut extensions, 0x002b, &[4, 0x03, 0x04, 0x03, 0x03]);
        push_extension(&mut extensions, 0x0010, &alpn_extension(b"http/1.1"));
        push_extension(&mut extensions, 0x000d, &[0, 4, 0x04, 0x01, 0x04, 0x03]);
        body.extend_from_slice(&(extensions.len() as u16).to_be_bytes());
        body.extend_from_slice(&extensions);

        let mut handshake = vec![
            1,
            ((body.len() >> 16) & 0xff) as u8,
            ((body.len() >> 8) & 0xff) as u8,
            (body.len() & 0xff) as u8,
        ];
        handshake.extend_from_slice(&body);
        handshake
    }

    fn minimal_client_hello_handshake(empty_extensions_block: bool) -> Vec<u8> {
        let mut body = Vec::new();
        body.extend_from_slice(&0x0301u16.to_be_bytes());
        body.extend_from_slice(&[1u8; 32]);
        body.push(2);
        body.extend_from_slice(&[0xaa, 0xbb]);
        body.extend_from_slice(&2u16.to_be_bytes());
        body.extend_from_slice(&0x002fu16.to_be_bytes());
        body.push(1);
        body.push(0);
        if empty_extensions_block {
            body.extend_from_slice(&0u16.to_be_bytes());
        }

        let mut handshake = vec![
            1,
            ((body.len() >> 16) & 0xff) as u8,
            ((body.len() >> 8) & 0xff) as u8,
            (body.len() & 0xff) as u8,
        ];
        handshake.extend_from_slice(&body);
        handshake
    }

    fn push_extension(dst: &mut Vec<u8>, typ: u16, data: &[u8]) {
        dst.extend_from_slice(&typ.to_be_bytes());
        dst.extend_from_slice(&(data.len() as u16).to_be_bytes());
        dst.extend_from_slice(data);
    }

    fn sni_extension(name: &str) -> Vec<u8> {
        let mut name_entry = Vec::new();
        name_entry.push(0);
        name_entry.extend_from_slice(&(name.len() as u16).to_be_bytes());
        name_entry.extend_from_slice(name.as_bytes());

        let mut extension = Vec::new();
        extension.extend_from_slice(&(name_entry.len() as u16).to_be_bytes());
        extension.extend_from_slice(&name_entry);
        extension
    }

    fn alpn_extension(protocol: &[u8]) -> Vec<u8> {
        let mut protocol_list = Vec::new();
        protocol_list.push(protocol.len() as u8);
        protocol_list.extend_from_slice(protocol);

        let mut extension = Vec::new();
        extension.extend_from_slice(&(protocol_list.len() as u16).to_be_bytes());
        extension.extend_from_slice(&protocol_list);
        extension
    }
}
