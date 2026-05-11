use core::fmt::Write as _;
use std::fs;
use std::io::{self, Read};
use std::path::PathBuf;

use anyhow::{Context, Result, anyhow, bail};
use clap::Parser;
use serde::Deserialize;
use serde_json::Value;

#[derive(Parser)]
#[command(about = "Decode ja4plus structured ClientHello JSON dumps as readable text")]
struct Args {
    /// Read JSON from a file instead of stdin. Use '-' for stdin.
    input: Option<PathBuf>,

    /// Include raw extension payload hex after decoded extension text.
    #[arg(long)]
    raw_extensions: bool,
}

#[derive(Debug)]
struct DumpInput {
    remote_addr: Option<String>,
    local_addr: Option<String>,
    ja4: Option<String>,
    hello: StructuredClientHelloInput,
}

#[derive(Debug, Deserialize)]
struct StructuredClientHelloInput {
    handshake_type: String,
    legacy_version: String,
    random_hex: String,
    session_id_hex: String,
    cipher_suites: Vec<String>,
    compression_methods: Vec<String>,
    extensions_present: bool,
    extensions: Vec<StructuredExtensionInput>,
    handshake_bytes: usize,
}

#[derive(Debug, Deserialize)]
struct StructuredExtensionInput {
    #[serde(rename = "type")]
    extension_type: String,
    payload_hex: String,
}

fn main() -> Result<()> {
    let args = Args::parse();
    let input = read_input(args.input.as_ref())?;
    let dumps = parse_inputs(&input)?;

    for (idx, dump) in dumps.iter().enumerate() {
        if idx > 0 {
            println!();
        }
        print!("{}", render_dump(dump, args.raw_extensions)?);
    }

    Ok(())
}

fn read_input(path: Option<&PathBuf>) -> Result<String> {
    match path {
        Some(path) if path.as_os_str() != "-" => {
            let path_text = path.to_string_lossy();
            if !path.exists() && looks_like_json(path_text.trim_start()) {
                return Ok(path_text.into_owned());
            }

            fs::read_to_string(path).with_context(|| format!("failed to read {}", path.display()))
        }
        _ => {
            let mut input = String::new();
            io::stdin()
                .read_to_string(&mut input)
                .context("failed to read stdin")?;
            Ok(input)
        }
    }
}

fn looks_like_json(input: &str) -> bool {
    input.starts_with('{') || input.starts_with('[')
}

fn parse_inputs(input: &str) -> Result<Vec<DumpInput>> {
    let input = input.trim();
    if input.is_empty() {
        bail!("no JSON input provided");
    }

    match parse_json_stream(input) {
        Ok(values) => {
            let mut dumps = Vec::new();
            for value in values {
                dumps.extend(parse_json_value(value)?);
            }
            Ok(dumps)
        }
        Err(stream_err) => {
            let extracted = parse_embedded_json_values(input)?;
            if !extracted.is_empty() {
                return Ok(extracted);
            }

            Err(enhance_json_error(input, stream_err))
        }
    }
}

fn parse_json_stream(input: &str) -> Result<Vec<Value>, serde_json::Error> {
    let mut values = Vec::new();
    let stream = serde_json::Deserializer::from_str(input).into_iter::<Value>();
    for value in stream {
        values.push(value?);
    }

    Ok(values)
}

fn parse_embedded_json_values(input: &str) -> Result<Vec<DumpInput>> {
    let mut dumps = Vec::new();
    for value in extract_json_values(input) {
        let Ok(value) = serde_json::from_str::<Value>(value) else {
            continue;
        };
        dumps.extend(parse_json_value(value)?);
    }
    Ok(dumps)
}

fn extract_json_values(input: &str) -> Vec<&str> {
    let mut values = Vec::new();
    let mut start = None;
    let mut depth = 0usize;
    let mut in_string = false;
    let mut escaped = false;

    for (idx, byte) in input.bytes().enumerate() {
        if start.is_none() {
            if matches!(byte, b'{' | b'[') {
                start = Some(idx);
                depth = 1;
            }
            continue;
        }

        if in_string {
            if escaped {
                escaped = false;
            } else if byte == b'\\' {
                escaped = true;
            } else if byte == b'"' {
                in_string = false;
            }
            continue;
        }

        match byte {
            b'"' => in_string = true,
            b'{' | b'[' => depth += 1,
            b'}' | b']' => {
                depth = depth.saturating_sub(1);
                if depth == 0 {
                    let value_start = start.take().unwrap();
                    values.push(&input[value_start..=idx]);
                }
            }
            _ => {}
        }
    }

    values
}

fn enhance_json_error(input: &str, err: serde_json::Error) -> anyhow::Error {
    let mut message = format!("failed to parse JSON input: {err}");
    if err.is_eof() {
        message.push_str("; input ended before a complete JSON value was read");
        if input.len() >= 4090 && input.len() <= 4096 {
            message.push_str(
                "; received about 4096 bytes, which usually means a long JSON line was pasted into an interactive terminal and got truncated. Put the capture in a file or pipe it, for example: cargo run -p ja4plus --bin ja4dump -- capture.jsonl",
            );
        }
    }
    anyhow!(message)
}

fn parse_json_value(value: Value) -> Result<Vec<DumpInput>> {
    match value {
        Value::Array(values) => values
            .into_iter()
            .map(dump_from_value)
            .collect::<Result<Vec<_>>>(),
        value => Ok(vec![dump_from_value(value)?]),
    }
}

fn dump_from_value(value: Value) -> Result<DumpInput> {
    let remote_addr = string_field(&value, "remote_addr");
    let local_addr = string_field(&value, "local_addr");
    let ja4 = string_field(&value, "ja4");

    let hello_value = value
        .get("client_hello_structured")
        .cloned()
        .unwrap_or(value);
    let hello = serde_json::from_value::<StructuredClientHelloInput>(hello_value)
        .context("input does not contain a valid client_hello_structured object")?;

    Ok(DumpInput {
        remote_addr,
        local_addr,
        ja4,
        hello,
    })
}

fn string_field(value: &Value, field: &str) -> Option<String> {
    value
        .get(field)
        .and_then(Value::as_str)
        .map(ToOwned::to_owned)
}

fn render_dump(dump: &DumpInput, raw_extensions: bool) -> Result<String> {
    let hello = &dump.hello;
    let mut out = String::new();

    writeln!(out, "ClientHello")?;
    if let Some(remote_addr) = &dump.remote_addr {
        writeln!(out, "  Remote: {remote_addr}")?;
    }
    if let Some(local_addr) = &dump.local_addr {
        writeln!(out, "  Local: {local_addr}")?;
    }
    if let Some(ja4) = &dump.ja4 {
        writeln!(out, "  JA4: {ja4}")?;
        writeln!(out, "  JA4 decoded: {}", describe_ja4(ja4))?;
    }

    let handshake_type = parse_hex_u8(&hello.handshake_type)?;
    let legacy_version = parse_hex_u16(&hello.legacy_version)?;
    let random = parse_hex_bytes(&hello.random_hex)?;
    let session_id = parse_hex_bytes(&hello.session_id_hex)?;
    let compression_methods = parse_hex_u8_list(&hello.compression_methods)?;

    writeln!(
        out,
        "  Handshake type: {} ({})",
        hello.handshake_type,
        handshake_type_name(handshake_type)
    )?;
    writeln!(
        out,
        "  Legacy version: {} ({})",
        hello.legacy_version,
        tls_version_name(legacy_version)
    )?;
    writeln!(out, "  Handshake bytes: {}", hello.handshake_bytes)?;
    writeln!(out, "  Random: {}", hex_bytes(&random))?;
    writeln!(
        out,
        "  Session ID: {} bytes{}",
        session_id.len(),
        if session_id.is_empty() {
            String::new()
        } else {
            format!(" ({})", hex_bytes(&session_id))
        }
    )?;
    writeln!(out, "  Compression methods:")?;
    for method in compression_methods {
        writeln!(
            out,
            "    0x{method:02x} {}",
            compression_method_name(method)
        )?;
    }

    writeln!(out)?;
    writeln!(out, "Cipher Suites:")?;
    for suite in parse_hex_u16_list(&hello.cipher_suites)? {
        writeln!(out, "  0x{suite:04x} {}", cipher_suite_name(suite))?;
    }

    writeln!(out)?;
    writeln!(out, "Extensions:")?;
    if !hello.extensions_present {
        writeln!(out, "  extension block absent")?;
        return Ok(out);
    }

    if hello.extensions.is_empty() {
        writeln!(out, "  extension block present but empty")?;
        return Ok(out);
    }

    for extension in &hello.extensions {
        let extension_type = parse_hex_u16(&extension.extension_type)?;
        let payload = parse_hex_bytes(&extension.payload_hex)?;
        writeln!(
            out,
            "  0x{extension_type:04x} {}:",
            extension_name(extension_type)
        )?;
        match decode_extension(extension_type, &payload) {
            Ok(lines) if lines.is_empty() => {
                writeln!(out, "    empty")?;
            }
            Ok(lines) => {
                for line in lines {
                    writeln!(out, "    {line}")?;
                }
            }
            Err(err) => {
                writeln!(out, "    undecoded: {err}")?;
                writeln!(out, "    payload: {} bytes", payload.len())?;
            }
        }

        if raw_extensions {
            writeln!(out, "    raw: {}", hex_bytes(&payload))?;
        }
    }

    Ok(out)
}

fn describe_ja4(ja4: &str) -> String {
    let Some(prefix) = ja4.split('_').next() else {
        return "unrecognized".to_owned();
    };
    if prefix.len() < 10 || !prefix.starts_with('t') {
        return "unrecognized".to_owned();
    }

    let version = &prefix[1..3];
    let sni = prefix.as_bytes()[3] as char;
    let cipher_count = &prefix[4..6];
    let extension_count = &prefix[6..8];
    let alpn = &prefix[8..];

    format!(
        "TLS {}, {}, {cipher_count} non-GREASE cipher suites, {extension_count} non-GREASE extensions, ALPN {}",
        ja4_tls_version(version),
        match sni {
            'd' => "SNI present",
            'i' => "SNI absent",
            _ => "SNI status unknown",
        },
        ja4_alpn(alpn)
    )
}

fn decode_extension(extension_type: u16, payload: &[u8]) -> Result<Vec<String>> {
    match extension_type {
        0x0000 => decode_server_name(payload),
        0x0005 => decode_status_request(payload),
        0x000a => decode_supported_groups(payload),
        0x000b => decode_ec_point_formats(payload),
        0x000d => decode_signature_algorithms(payload),
        0x0010 => decode_alpn(payload),
        0x0012 | 0x0017 | 0x0023 => Ok(empty_or_len(payload)),
        0x001b => decode_compress_certificate(payload),
        0x001c => decode_record_size_limit(payload),
        0x0022 => decode_delegated_credentials(payload),
        0x002b => decode_supported_versions(payload),
        0x002d => decode_psk_key_exchange_modes(payload),
        0x0033 => decode_key_share(payload),
        0x4469 | 0x44cd => decode_application_settings(payload),
        0xfe0d => decode_ech(payload),
        0xff01 => decode_renegotiation_info(payload),
        _ => Ok(vec![format!("payload: {} bytes", payload.len())]),
    }
}

fn decode_server_name(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let list = cursor.take_u16_len_prefixed("server_name list")?;
    let mut names = Vec::new();
    let mut list_cursor = Cursor::new(list);
    while !list_cursor.is_empty() {
        let name_type = list_cursor.take_u8("name_type")?;
        let name = list_cursor.take_u16_len_prefixed("server_name")?;
        let rendered = if name_type == 0 {
            format!("dns_name: {}", String::from_utf8_lossy(name))
        } else {
            format!("name_type 0x{name_type:02x}: {}", hex_bytes(name))
        };
        names.push(rendered);
    }
    cursor.finish()?;
    Ok(names)
}

fn decode_status_request(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let status_type = cursor.take_u8("status_type")?;
    let mut lines = vec![format!(
        "status_type: 0x{status_type:02x} {}",
        status_request_name(status_type)
    )];

    if status_type == 1 {
        let responder_ids = cursor.take_u16_len_prefixed("responder_id_list")?;
        let request_extensions = cursor.take_u16_len_prefixed("request_extensions")?;
        lines.push(format!("responder_id_list: {} bytes", responder_ids.len()));
        lines.push(format!(
            "request_extensions: {} bytes",
            request_extensions.len()
        ));
    } else {
        lines.push(format!("payload tail: {} bytes", cursor.remaining()));
        cursor.take(cursor.remaining(), "payload tail")?;
    }

    cursor.finish()?;
    Ok(lines)
}

fn decode_supported_groups(payload: &[u8]) -> Result<Vec<String>> {
    decode_u16_vector(payload, 2, "supported_groups")?
        .into_iter()
        .map(|group| Ok(format!("0x{group:04x} {}", named_group_name(group))))
        .collect()
}

fn decode_ec_point_formats(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let formats = cursor.take_u8_len_prefixed("ec_point_formats")?;
    let lines = formats
        .iter()
        .map(|format| format!("0x{format:02x} {}", ec_point_format_name(*format)))
        .collect();
    cursor.finish()?;
    Ok(lines)
}

fn decode_signature_algorithms(payload: &[u8]) -> Result<Vec<String>> {
    decode_u16_vector(payload, 2, "signature_algorithms")?
        .into_iter()
        .map(|scheme| Ok(format!("0x{scheme:04x} {}", signature_scheme_name(scheme))))
        .collect()
}

fn decode_alpn(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let protocol_list = cursor.take_u16_len_prefixed("alpn_protocol_list")?;
    let mut protocol_cursor = Cursor::new(protocol_list);
    let mut lines = Vec::new();
    while !protocol_cursor.is_empty() {
        let protocol = protocol_cursor.take_u8_len_prefixed("alpn_protocol")?;
        lines.push(format!("protocol: {}", printable_bytes(protocol)));
    }
    cursor.finish()?;
    Ok(lines)
}

fn decode_compress_certificate(payload: &[u8]) -> Result<Vec<String>> {
    decode_u16_vector(payload, 1, "compress_certificate algorithms")?
        .into_iter()
        .map(|algorithm| {
            Ok(format!(
                "0x{algorithm:04x} {}",
                certificate_compression_name(algorithm)
            ))
        })
        .collect()
}

fn decode_record_size_limit(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let limit = cursor.take_u16("record_size_limit")?;
    cursor.finish()?;
    Ok(vec![format!("{limit} bytes")])
}

fn decode_delegated_credentials(payload: &[u8]) -> Result<Vec<String>> {
    decode_u16_vector(payload, 2, "delegated_credentials algorithms")?
        .into_iter()
        .map(|scheme| Ok(format!("0x{scheme:04x} {}", signature_scheme_name(scheme))))
        .collect()
}

fn decode_supported_versions(payload: &[u8]) -> Result<Vec<String>> {
    decode_u16_vector(payload, 1, "supported_versions")?
        .into_iter()
        .map(|version| Ok(format!("0x{version:04x} {}", tls_version_name(version))))
        .collect()
}

fn decode_psk_key_exchange_modes(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let modes = cursor.take_u8_len_prefixed("psk_key_exchange_modes")?;
    let lines = modes
        .iter()
        .map(|mode| format!("0x{mode:02x} {}", psk_key_exchange_mode_name(*mode)))
        .collect();
    cursor.finish()?;
    Ok(lines)
}

fn decode_key_share(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let shares = cursor.take_u16_len_prefixed("client_shares")?;
    let mut share_cursor = Cursor::new(shares);
    let mut lines = Vec::new();
    while !share_cursor.is_empty() {
        let group = share_cursor.take_u16("group")?;
        let key_exchange = share_cursor.take_u16_len_prefixed("key_exchange")?;
        lines.push(format!(
            "0x{group:04x} {}, {}-byte key share",
            named_group_name(group),
            key_exchange.len()
        ));
    }
    cursor.finish()?;
    Ok(lines)
}

fn decode_application_settings(payload: &[u8]) -> Result<Vec<String>> {
    decode_alpn(payload)
}

fn decode_ech(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let ech_type = cursor.take_u8("ech_type")?;
    if ech_type == 1 {
        cursor.finish()?;
        return Ok(vec!["ECH inner marker".to_owned()]);
    }

    let kdf_id = cursor.take_u16("kdf_id")?;
    let aead_id = cursor.take_u16("aead_id")?;
    let config_id = cursor.take_u8("config_id")?;
    let enc = cursor.take_u16_len_prefixed("enc")?;
    let encrypted_payload = cursor.take_u16_len_prefixed("payload")?;
    cursor.finish()?;

    Ok(vec![
        format!("type: 0x{ech_type:02x} ECHClientHelloOuter"),
        format!("KDF: 0x{kdf_id:04x} {}", hpke_kdf_name(kdf_id)),
        format!("AEAD: 0x{aead_id:04x} {}", hpke_aead_name(aead_id)),
        format!("config_id: 0x{config_id:02x}"),
        format!("enc: {} bytes", enc.len()),
        format!("encrypted payload: {} bytes", encrypted_payload.len()),
    ])
}

fn decode_renegotiation_info(payload: &[u8]) -> Result<Vec<String>> {
    let mut cursor = Cursor::new(payload);
    let renegotiated_connection = cursor.take_u8_len_prefixed("renegotiated_connection")?;
    cursor.finish()?;
    if renegotiated_connection.is_empty() {
        Ok(vec!["renegotiated_connection: empty".to_owned()])
    } else {
        Ok(vec![format!(
            "renegotiated_connection: {}",
            hex_bytes(renegotiated_connection)
        )])
    }
}

fn decode_u16_vector(payload: &[u8], prefix_len: usize, name: &str) -> Result<Vec<u16>> {
    let mut cursor = Cursor::new(payload);
    let vector = match prefix_len {
        1 => cursor.take_u8_len_prefixed(name)?,
        2 => cursor.take_u16_len_prefixed(name)?,
        _ => bail!("unsupported vector prefix length {prefix_len}"),
    };
    if vector.len() % 2 != 0 {
        bail!("{name} has an odd byte length");
    }
    let values = vector
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect();
    cursor.finish()?;
    Ok(values)
}

fn empty_or_len(payload: &[u8]) -> Vec<String> {
    if payload.is_empty() {
        Vec::new()
    } else {
        vec![format!("payload: {} bytes", payload.len())]
    }
}

#[derive(Clone, Copy)]
struct Cursor<'a> {
    bytes: &'a [u8],
    offset: usize,
}

impl<'a> Cursor<'a> {
    fn new(bytes: &'a [u8]) -> Self {
        Self { bytes, offset: 0 }
    }

    fn is_empty(&self) -> bool {
        self.offset == self.bytes.len()
    }

    fn remaining(&self) -> usize {
        self.bytes
            .len()
            .saturating_sub(self.offset)
    }

    fn take(&mut self, len: usize, name: &str) -> Result<&'a [u8]> {
        let end = self
            .offset
            .checked_add(len)
            .ok_or_else(|| anyhow!("{name} length overflow"))?;
        if end > self.bytes.len() {
            bail!("{name} exceeds remaining payload");
        }
        let taken = &self.bytes[self.offset..end];
        self.offset = end;
        Ok(taken)
    }

    fn take_u8(&mut self, name: &str) -> Result<u8> {
        Ok(self.take(1, name)?[0])
    }

    fn take_u16(&mut self, name: &str) -> Result<u16> {
        let bytes = self.take(2, name)?;
        Ok(u16::from_be_bytes([bytes[0], bytes[1]]))
    }

    fn take_u8_len_prefixed(&mut self, name: &str) -> Result<&'a [u8]> {
        let len = self.take_u8(name)? as usize;
        self.take(len, name)
    }

    fn take_u16_len_prefixed(&mut self, name: &str) -> Result<&'a [u8]> {
        let len = self.take_u16(name)? as usize;
        self.take(len, name)
    }

    fn finish(&self) -> Result<()> {
        if !self.is_empty() {
            bail!("{} trailing bytes", self.remaining());
        }
        Ok(())
    }
}

fn parse_hex_u8_list(values: &[String]) -> Result<Vec<u8>> {
    values
        .iter()
        .map(|value| parse_hex_u8(value))
        .collect()
}

fn parse_hex_u16_list(values: &[String]) -> Result<Vec<u16>> {
    values
        .iter()
        .map(|value| parse_hex_u16(value))
        .collect()
}

fn parse_hex_u8(value: &str) -> Result<u8> {
    let parsed = parse_hex_u64(value)?;
    u8::try_from(parsed).with_context(|| format!("{value} is too large for u8"))
}

fn parse_hex_u16(value: &str) -> Result<u16> {
    let parsed = parse_hex_u64(value)?;
    u16::try_from(parsed).with_context(|| format!("{value} is too large for u16"))
}

fn parse_hex_u64(value: &str) -> Result<u64> {
    let hex = value
        .strip_prefix("0x")
        .unwrap_or(value);
    u64::from_str_radix(hex, 16).with_context(|| format!("invalid hex value {value}"))
}

fn parse_hex_bytes(value: &str) -> Result<Vec<u8>> {
    let hex = value
        .strip_prefix("0x")
        .unwrap_or(value);
    if hex.len() % 2 != 0 {
        bail!("hex byte string has odd length");
    }

    let mut bytes = Vec::with_capacity(hex.len() / 2);
    for idx in (0..hex.len()).step_by(2) {
        let byte = u8::from_str_radix(&hex[idx..idx + 2], 16)
            .with_context(|| format!("invalid hex byte at offset {idx}"))?;
        bytes.push(byte);
    }
    Ok(bytes)
}

fn hex_bytes(bytes: &[u8]) -> String {
    bytes
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn printable_bytes(bytes: &[u8]) -> String {
    match core::str::from_utf8(bytes) {
        Ok(value) => value.to_owned(),
        Err(_) => format!("0x{}", hex_bytes(bytes)),
    }
}

fn is_grease(value: u16) -> bool {
    value & 0x000f == 0x000a && value >> 8 == value & 0x00ff
}

fn handshake_type_name(value: u8) -> &'static str {
    match value {
        1 => "ClientHello",
        _ => "unknown",
    }
}

fn ja4_tls_version(value: &str) -> &'static str {
    match value {
        "13" => "1.3",
        "12" => "1.2",
        "11" => "1.1",
        "10" => "1.0",
        "s3" => "SSL 3.0",
        "s2" => "SSL 2.0",
        "d1" => "DTLS 1.0",
        "d2" => "DTLS 1.2",
        "d3" => "DTLS 1.3",
        _ => "unknown",
    }
}

fn ja4_alpn(value: &str) -> String {
    match value {
        "00" => "absent".to_owned(),
        "h2" => "h2".to_owned(),
        "h1" => "http/1.1".to_owned(),
        _ => value.to_owned(),
    }
}

fn tls_version_name(value: u16) -> &'static str {
    match value {
        0x0304 => "TLS 1.3",
        0x0303 => "TLS 1.2",
        0x0302 => "TLS 1.1",
        0x0301 => "TLS 1.0",
        0x0300 => "SSL 3.0",
        value if is_grease(value) => "GREASE",
        _ => "unknown",
    }
}

fn cipher_suite_name(value: u16) -> &'static str {
    match value {
        0x1301 => "TLS_AES_128_GCM_SHA256",
        0x1302 => "TLS_AES_256_GCM_SHA384",
        0x1303 => "TLS_CHACHA20_POLY1305_SHA256",
        0xc02b => "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
        0xc02c => "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384",
        0xc02f => "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
        0xc030 => "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
        0xcca8 => "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256",
        0xcca9 => "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256",
        0xc009 => "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA",
        0xc00a => "TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA",
        0xc013 => "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA",
        0xc014 => "TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA",
        0x009c => "TLS_RSA_WITH_AES_128_GCM_SHA256",
        0x009d => "TLS_RSA_WITH_AES_256_GCM_SHA384",
        0x002f => "TLS_RSA_WITH_AES_128_CBC_SHA",
        0x0035 => "TLS_RSA_WITH_AES_256_CBC_SHA",
        value if is_grease(value) => "GREASE",
        _ => "unknown",
    }
}

fn extension_name(value: u16) -> &'static str {
    match value {
        0x0000 => "server_name",
        0x0005 => "status_request",
        0x000a => "supported_groups",
        0x000b => "ec_point_formats",
        0x000d => "signature_algorithms",
        0x0010 => "application_layer_protocol_negotiation",
        0x0012 => "signed_certificate_timestamp",
        0x0017 => "extended_master_secret",
        0x001b => "compress_certificate",
        0x001c => "record_size_limit",
        0x0022 => "delegated_credentials",
        0x0023 => "session_ticket",
        0x002b => "supported_versions",
        0x002d => "psk_key_exchange_modes",
        0x0033 => "key_share",
        0x4469 => "application_settings_old",
        0x44cd => "application_settings",
        0xfe0d => "encrypted_client_hello",
        0xff01 => "renegotiation_info",
        value if is_grease(value) => "GREASE",
        _ => "unknown",
    }
}

fn named_group_name(value: u16) -> &'static str {
    match value {
        0x0017 => "secp256r1",
        0x0018 => "secp384r1",
        0x0019 => "secp521r1",
        0x001d => "x25519",
        0x001e => "x448",
        0x0100 => "ffdhe2048",
        0x0101 => "ffdhe3072",
        0x0102 => "ffdhe4096",
        0x0103 => "ffdhe6144",
        0x0104 => "ffdhe8192",
        0x0200 => "MLKEM512",
        0x0201 => "MLKEM768",
        0x0202 => "MLKEM1024",
        0x11eb => "secp256r1MLKEM768",
        0x11ec => "X25519MLKEM768",
        0x11ed => "secp384r1MLKEM1024",
        value if is_grease(value) => "GREASE",
        _ => "unknown",
    }
}

fn ec_point_format_name(value: u8) -> &'static str {
    match value {
        0 => "uncompressed",
        1 => "ansiX962_compressed_prime",
        2 => "ansiX962_compressed_char2",
        _ => "unknown",
    }
}

fn status_request_name(value: u8) -> &'static str {
    match value {
        1 => "OCSP",
        _ => "unknown",
    }
}

fn signature_scheme_name(value: u16) -> &'static str {
    match value {
        0x0201 => "rsa_pkcs1_sha1",
        0x0203 => "ecdsa_sha1",
        0x0401 => "rsa_pkcs1_sha256",
        0x0403 => "ecdsa_secp256r1_sha256",
        0x0501 => "rsa_pkcs1_sha384",
        0x0503 => "ecdsa_secp384r1_sha384",
        0x0601 => "rsa_pkcs1_sha512",
        0x0603 => "ecdsa_secp521r1_sha512",
        0x0804 => "rsa_pss_rsae_sha256",
        0x0805 => "rsa_pss_rsae_sha384",
        0x0806 => "rsa_pss_rsae_sha512",
        value if is_grease(value) => "GREASE",
        _ => "unknown",
    }
}

fn compression_method_name(value: u8) -> &'static str {
    match value {
        0 => "null",
        _ => "unknown",
    }
}

fn certificate_compression_name(value: u16) -> &'static str {
    match value {
        1 => "zlib",
        2 => "brotli",
        3 => "zstd",
        _ => "unknown",
    }
}

fn psk_key_exchange_mode_name(value: u8) -> &'static str {
    match value {
        0 => "psk_ke",
        1 => "psk_dhe_ke",
        _ => "unknown",
    }
}

fn hpke_kdf_name(value: u16) -> &'static str {
    match value {
        0x0001 => "HKDF-SHA256",
        0x0002 => "HKDF-SHA384",
        0x0003 => "HKDF-SHA512",
        _ => "unknown",
    }
}

fn hpke_aead_name(value: u16) -> &'static str {
    match value {
        0x0001 => "AES-128-GCM",
        0x0002 => "AES-256-GCM",
        0x0003 => "ChaCha20-Poly1305",
        _ => "unknown",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_full_ja4plus_json_line() {
        let input = r#"{"remote_addr":"127.0.0.1:1","local_addr":"127.0.0.1:8443","ja4":"t13i0203h2_aaaaaaaaaaaa_bbbbbbbbbbbb","client_hello_structured":{"handshake_type":"0x01","legacy_version":"0x0303","random_hex":"0000000000000000000000000000000000000000000000000000000000000000","session_id_hex":"","cipher_suites":["0x1301","0x1302"],"compression_methods":["0x00"],"extensions_present":true,"extensions":[{"type":"0x0010","payload_hex":"000c02683208687474702f312e31"},{"type":"0x002b","payload_hex":"0403040303"},{"type":"0x0033","payload_hex":"0024001d0020aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},{"type":"0xfe0d","payload_hex":"0000010001000020bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb0002cafe"}],"handshake_bytes":123}}"#;

        let dumps = parse_inputs(input).unwrap();
        assert_eq!(dumps.len(), 1);

        let text = render_dump(&dumps[0], false).unwrap();
        assert!(text.contains("Remote: 127.0.0.1:1"));
        assert!(text.contains("JA4 decoded: TLS 1.3, SNI absent"));
        assert!(text.contains("0x1301 TLS_AES_128_GCM_SHA256"));
        assert!(text.contains("protocol: h2"));
        assert!(text.contains("0x001d x25519, 32-byte key share"));
        assert!(text.contains("AEAD: 0x0001 AES-128-GCM"));
        assert!(text.contains("encrypted payload: 2 bytes"));
    }

    #[test]
    fn parses_standalone_structured_dump() {
        let input = r#"{"handshake_type":"0x01","legacy_version":"0x0303","random_hex":"0000000000000000000000000000000000000000000000000000000000000000","session_id_hex":"abcd","cipher_suites":["0x1303"],"compression_methods":["0x00"],"extensions_present":false,"extensions":[],"handshake_bytes":47}"#;

        let dumps = parse_inputs(input).unwrap();
        let text = render_dump(&dumps[0], false).unwrap();
        assert!(text.contains("Session ID: 2 bytes (abcd)"));
        assert!(text.contains("extension block absent"));
    }

    #[test]
    fn parses_line_delimited_json() {
        let one = r#"{"handshake_type":"0x01","legacy_version":"0x0303","random_hex":"0000000000000000000000000000000000000000000000000000000000000000","session_id_hex":"","cipher_suites":[],"compression_methods":["0x00"],"extensions_present":false,"extensions":[],"handshake_bytes":43}"#;
        let input = format!("{one}\n{one}\n");

        let dumps = parse_inputs(&input).unwrap();
        assert_eq!(dumps.len(), 2);
    }

    #[test]
    fn parses_trailing_whitespace_and_same_line_json_stream() {
        let one = r#"{"handshake_type":"0x01","legacy_version":"0x0303","random_hex":"0000000000000000000000000000000000000000000000000000000000000000","session_id_hex":"","cipher_suites":[],"compression_methods":["0x00"],"extensions_present":false,"extensions":[],"handshake_bytes":43}"#;

        let dumps = parse_inputs(&format!("{one}\n")).unwrap();
        assert_eq!(dumps.len(), 1);

        let dumps = parse_inputs(&format!("{one} {one}\n")).unwrap();
        assert_eq!(dumps.len(), 2);
    }

    #[test]
    fn extracts_json_from_noisy_captured_text() {
        let one = r#"{"handshake_type":"0x01","legacy_version":"0x0303","random_hex":"0000000000000000000000000000000000000000000000000000000000000000","session_id_hex":"","cipher_suites":[],"compression_methods":["0x00"],"extensions_present":false,"extensions":[],"handshake_bytes":43}"#;
        let input = format!("warning: ignored text before JSON\n{one}\nignored text after JSON\n");

        let dumps = parse_inputs(&input).unwrap();
        assert_eq!(dumps.len(), 1);
    }

    #[test]
    fn explains_likely_terminal_paste_truncation() {
        let mut input = r#"{"client_hello_structured":{"handshake_type":""#.to_owned();
        input.push_str(&"0".repeat(4095 - input.len()));
        assert_eq!(input.len(), 4095);

        let err = parse_inputs(&input)
            .unwrap_err()
            .to_string();
        assert!(err.contains("pasted into an interactive terminal"));
    }
}
