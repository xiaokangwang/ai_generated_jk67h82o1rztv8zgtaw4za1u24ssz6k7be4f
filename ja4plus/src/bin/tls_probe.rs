use core::time::Duration;
use std::io::{self, Read, Write};
use std::net::{TcpListener, TcpStream};

use anyhow::{Context, Result, anyhow, bail};
use clap::{Parser, ValueEnum};
use ring::agreement;
use ring::rand::{SecureRandom, SystemRandom};
use serde::Serialize;

const DEFAULT_LISTEN: &str = "127.0.0.1:8443";
const DEFAULT_TIMEOUT_MS: u64 = 3000;
const MAX_CLIENT_HELLO_BYTES: usize = 256 * 1024;
const MAX_REACTION_RECORDS: usize = 6;
const TLS_RECORD_HANDSHAKE: u8 = 22;
const TLS_RECORD_ALERT: u8 = 21;
const TLS_RECORD_APPLICATION_DATA: u8 = 23;
const TLS13: u16 = 0x0304;
const TLS12: u16 = 0x0303;
const DEFAULT_TLS13_CIPHER: u16 = 0x1301;
const DEFAULT_TLS12_CIPHER: u16 = 0xc02f;
const DEFAULT_TLS12_ECDSA_CIPHER: u16 = 0xc02b;
const DEFAULT_GROUP: u16 = 0x001d;
const DEFAULT_TLS12_ECDHE_GROUP: u16 = 0x0017;
const DEFAULT_EXTENSION: u16 = 0x0039;
const DEFAULT_SIGNATURE_SCHEME: u16 = 0x0203;

const ECDSA_P256_END_DER: &[u8] = include_bytes!("../../../test-ca/ecdsa-p256/end.der");
const ECDSA_P256_INTER_DER: &[u8] = include_bytes!("../../../test-ca/ecdsa-p256/inter.der");
const RSA_2048_END_DER: &[u8] = include_bytes!("../../../test-ca/rsa-2048/end.der");
const RSA_2048_INTER_DER: &[u8] = include_bytes!("../../../test-ca/rsa-2048/inter.der");

const HRR_RANDOM: [u8; 32] = [
    0xcf, 0x21, 0xad, 0x74, 0xe5, 0x9a, 0x61, 0x11, 0xbe, 0x1d, 0x8c, 0x02, 0x1e, 0x65, 0xb8, 0x91,
    0xc2, 0xa2, 0x11, 0x16, 0x7a, 0xbb, 0x8c, 0x5e, 0x07, 0x9e, 0x09, 0xe2, 0xc8, 0xa8, 0x33, 0x9c,
];

#[derive(Parser)]
#[command(about = "Active TLS ClientHello probe server for comparing browser and craftls behavior")]
struct Args {
    /// TCP address to listen on.
    #[arg(long, default_value = DEFAULT_LISTEN)]
    listen: String,

    /// Exit after one accepted connection.
    #[arg(long)]
    once: bool,

    /// Probe to run against each accepted client.
    #[arg(long, value_enum, default_value_t = Probe::HrrGroup)]
    probe: Probe,

    /// Selected cipher suite, for example 0x1301 or 0xc02f.
    #[arg(long, value_parser = parse_u16_arg)]
    cipher: Option<u16>,

    /// Selected TLS group, for example 0x001d, 0x0017, 0x0100, 0x11ec, or 0x0a0a.
    #[arg(long, value_parser = parse_u16_arg)]
    group: Option<u16>,

    /// Selected protocol version for the tls13-version probe.
    #[arg(long, value_parser = parse_u16_arg)]
    version: Option<u16>,

    /// Selected TLS 1.2 SignatureAndHashAlgorithm for tls12-signature-scheme.
    #[arg(long, value_parser = parse_u16_arg, default_value_t = DEFAULT_SIGNATURE_SCHEME)]
    signature_scheme: u16,

    /// Certificate chain to send for TLS 1.2 signature probes.
    #[arg(long, value_enum, default_value_t = ProbeCertChain::EcdsaP256)]
    cert_chain: ProbeCertChain,

    /// Explicit ServerKeyExchange signature bytes, encoded as hex.
    ///
    /// By default the probe sends a syntactically valid but intentionally bad
    /// signature for the selected algorithm.
    #[arg(long)]
    ske_signature_hex: Option<String>,

    /// Selected ALPN protocol for the tls13-alpn probe.
    #[arg(long)]
    alpn: Option<String>,

    /// ServerHello extension type for unsolicited-extension probes.
    #[arg(long, value_parser = parse_u16_arg)]
    extension: Option<u16>,

    /// Hex payload for unsolicited ServerHello extension probes.
    #[arg(long, default_value = "")]
    extension_payload_hex: String,

    /// Explicit key_share bytes for tls13-key-share, encoded as hex.
    #[arg(long)]
    key_share_hex: Option<String>,

    /// Key share length to synthesize when key_share_hex is not supplied.
    #[arg(long)]
    key_share_len: Option<usize>,

    /// Fatal TLS alert description to send with the alert probe.
    #[arg(long, value_parser = parse_u8_arg, default_value = "47")]
    alert_description: u8,

    /// Echo the ClientHello session ID in TLS 1.2 ServerHello probes.
    ///
    /// Off by default because echoing the TLS 1.3 compatibility session ID
    /// looks like TLS 1.2 session resumption.
    #[arg(long)]
    tls12_echo_session_id: bool,

    /// Refuse to send a selection that was not advertised by the client.
    #[arg(long)]
    require_offered: bool,

    /// Maximum accepted ClientHello handshake size in bytes.
    #[arg(long, default_value_t = MAX_CLIENT_HELLO_BYTES)]
    max_client_hello_bytes: usize,

    /// Read timeout used while waiting for client reaction.
    #[arg(long, default_value_t = DEFAULT_TIMEOUT_MS)]
    timeout_ms: u64,

    /// Number of TLS records to read after sending the probe.
    #[arg(long, default_value_t = MAX_REACTION_RECORDS)]
    max_reaction_records: usize,
}

#[derive(Clone, Copy, Debug, ValueEnum)]
enum Probe {
    /// Send a TLS 1.3 HelloRetryRequest selecting --group and observe the second ClientHello/alert.
    HrrGroup,
    /// Send a TLS 1.3 ServerHello selecting --cipher and a key_share.
    Tls13Cipher,
    /// Send a TLS 1.3 ServerHello selecting --group in key_share.
    Tls13KeyShare,
    /// Send a TLS 1.3 ServerHello with supported_versions set to --version.
    Tls13Version,
    /// Send a TLS 1.3 ServerHello selecting --alpn.
    Tls13Alpn,
    /// Send a TLS 1.3 ServerHello with an unsolicited extension.
    Tls13UnsolicitedExtension,
    /// Send a TLS 1.2 ServerHello selecting --cipher.
    Tls12Cipher,
    /// Send TLS 1.2 ServerHello, Certificate, and ServerKeyExchange with --signature-scheme.
    Tls12SignatureScheme,
    /// Send a TLS 1.2 ServerHello with an unsolicited extension.
    Tls12UnsolicitedExtension,
    /// Read ClientHello, then send a fatal alert.
    Alert,
}

impl Probe {
    fn as_str(self) -> &'static str {
        match self {
            Self::HrrGroup => "hrr-group",
            Self::Tls13Cipher => "tls13-cipher",
            Self::Tls13KeyShare => "tls13-key-share",
            Self::Tls13Version => "tls13-version",
            Self::Tls13Alpn => "tls13-alpn",
            Self::Tls13UnsolicitedExtension => "tls13-unsolicited-extension",
            Self::Tls12Cipher => "tls12-cipher",
            Self::Tls12SignatureScheme => "tls12-signature-scheme",
            Self::Tls12UnsolicitedExtension => "tls12-unsolicited-extension",
            Self::Alert => "alert",
        }
    }
}

#[derive(Clone, Copy, Debug, ValueEnum)]
enum ProbeCertChain {
    EcdsaP256,
    Rsa2048,
}

impl ProbeCertChain {
    fn chain(self) -> &'static [&'static [u8]] {
        match self {
            Self::EcdsaP256 => &[ECDSA_P256_END_DER, ECDSA_P256_INTER_DER],
            Self::Rsa2048 => &[RSA_2048_END_DER, RSA_2048_INTER_DER],
        }
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::EcdsaP256 => "ecdsa-p256",
            Self::Rsa2048 => "rsa-2048",
        }
    }
}

#[derive(Debug, Clone, Default)]
struct ClientHello {
    legacy_version: u16,
    session_id: Vec<u8>,
    cipher_suites: Vec<u16>,
    extensions: Vec<ClientExtension>,
    supported_versions: Vec<u16>,
    supported_groups: Vec<u16>,
    key_share_groups: Vec<u16>,
    signature_schemes: Vec<u16>,
    alpn_protocols: Vec<Vec<u8>>,
    server_name: Option<String>,
    handshake_len: usize,
}

#[derive(Debug, Clone)]
struct ClientExtension {
    extension_type: u16,
    payload: Vec<u8>,
}

#[derive(Serialize)]
struct Output {
    remote_addr: String,
    local_addr: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    client_hello: Option<ClientHelloOutput>,
    #[serde(skip_serializing_if = "Option::is_none")]
    probe: Option<ProbeOutput>,
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
    supported_groups: Vec<String>,
    key_share_groups: Vec<String>,
    signature_schemes: Vec<String>,
    handshake_bytes: usize,
}

#[derive(Serialize)]
struct ProbeOutput {
    name: &'static str,
    selected: Vec<SelectedOutput>,
    offered: Vec<OfferCheckOutput>,
    sent_records: Vec<RecordOutput>,
    reaction: ReactionOutput,
    notes: Vec<String>,
}

#[derive(Serialize)]
struct SelectedOutput {
    name: &'static str,
    value: String,
}

#[derive(Serialize)]
struct OfferCheckOutput {
    name: &'static str,
    value: String,
    offered: bool,
}

#[derive(Serialize)]
struct ReactionOutput {
    terminal: String,
    records: Vec<RecordOutput>,
}

#[derive(Serialize)]
struct RecordOutput {
    content_type: String,
    legacy_version: String,
    length: usize,
    summary: String,
}

fn main() -> Result<()> {
    let args = Args::parse();
    let listener = TcpListener::bind(&args.listen)
        .with_context(|| format!("failed to bind {}", args.listen))?;
    eprintln!("tls_probe listening on {}", listener.local_addr()?);

    for stream in listener.incoming() {
        let mut stream = match stream {
            Ok(stream) => stream,
            Err(err) => {
                eprintln!("accept failed: {err}");
                continue;
            }
        };

        let output = handle_connection(&mut stream, &args);
        serde_json::to_writer(io::stdout().lock(), &output)?;
        println!();

        if args.once {
            break;
        }
    }

    Ok(())
}

fn handle_connection(stream: &mut TcpStream, args: &Args) -> Output {
    let remote_addr = stream
        .peer_addr()
        .map(|addr| addr.to_string())
        .unwrap_or_default();
    let local_addr = stream
        .local_addr()
        .map(|addr| addr.to_string())
        .unwrap_or_default();
    let timeout = Duration::from_millis(args.timeout_ms);
    let _ = stream.set_read_timeout(Some(timeout));
    let _ = stream.set_write_timeout(Some(timeout));

    match read_client_hello(stream, args.max_client_hello_bytes).and_then(|handshake| {
        let hello = parse_client_hello_handshake(&handshake)?;
        let probe = run_probe(stream, &hello, args)?;
        Ok((hello, probe))
    }) {
        Ok((hello, probe)) => Output {
            remote_addr,
            local_addr,
            client_hello: Some(ClientHelloOutput::from(&hello)),
            probe: Some(probe),
            error: None,
        },
        Err(err) => Output {
            remote_addr,
            local_addr,
            client_hello: None,
            probe: None,
            error: Some(err.to_string()),
        },
    }
}

fn run_probe(stream: &mut TcpStream, hello: &ClientHello, args: &Args) -> Result<ProbeOutput> {
    let mut selected = Vec::new();
    let mut offered = Vec::new();
    let mut notes = Vec::new();
    let records = match args.probe {
        Probe::HrrGroup => {
            let group = selected_group(args);
            selected.push(selected_u16("group", group));
            let supported_groups_check =
                check_u16("supported_groups", group, &hello.supported_groups);
            let key_share_groups_check =
                check_u16("key_share_groups", group, &hello.key_share_groups);
            ensure_offered(args, &supported_groups_check, "supported_groups")?;
            if key_share_groups_check.offered {
                notes.push(
                    "The selected HRR group was already present in the original key_share extension; TLS 1.3 clients should reject this HRR as illegal."
                        .to_string(),
                );
            }
            offered.push(supported_groups_check);
            offered.push(key_share_groups_check);

            let cipher = selected_tls13_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));
            offered.push(check_u16("cipher_suites", cipher, &hello.cipher_suites));
            ensure_offered(args, offered.last().unwrap(), "cipher_suites")?;

            notes.push(
                "A browser should send a second ClientHello if it accepts this HRR selection."
                    .to_string(),
            );
            vec![server_hello_record(
                &hello.session_id,
                cipher,
                HRR_RANDOM,
                vec![
                    extension(0x002b, &u16_bytes(TLS13)),
                    extension(0x0033, &u16_bytes(group)),
                ],
                true,
            )?]
        }
        Probe::Tls13Cipher => {
            let cipher = selected_tls13_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));
            offered.push(check_u16("cipher_suites", cipher, &hello.cipher_suites));
            ensure_offered(args, offered.last().unwrap(), "cipher_suites")?;

            let group = selected_group(args);
            selected.push(selected_u16("group", group));
            offered.push(check_u16(
                "key_share_groups",
                group,
                &hello.key_share_groups,
            ));

            let key_share = selected_key_share(args, group)?;
            selected.push(SelectedOutput {
                name: "key_share_bytes",
                value: key_share.len().to_string(),
            });
            vec![tls13_server_hello_record(
                &hello.session_id,
                cipher,
                group,
                &key_share,
            )?]
        }
        Probe::Tls13KeyShare => {
            let cipher = selected_tls13_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));
            offered.push(check_u16("cipher_suites", cipher, &hello.cipher_suites));

            let group = selected_group(args);
            selected.push(selected_u16("group", group));
            offered.push(check_u16(
                "supported_groups",
                group,
                &hello.supported_groups,
            ));
            offered.push(check_u16(
                "key_share_groups",
                group,
                &hello.key_share_groups,
            ));
            ensure_offered(args, offered.last().unwrap(), "key_share_groups")?;

            let key_share = selected_key_share(args, group)?;
            selected.push(SelectedOutput {
                name: "key_share_bytes",
                value: key_share.len().to_string(),
            });
            vec![tls13_server_hello_record(
                &hello.session_id,
                cipher,
                group,
                &key_share,
            )?]
        }
        Probe::Tls13Version => {
            let cipher = selected_tls13_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));

            let version = args.version.unwrap_or(0x0305);
            selected.push(selected_u16("supported_versions", version));
            offered.push(check_u16(
                "supported_versions",
                version,
                &hello.supported_versions,
            ));
            ensure_offered(args, offered.last().unwrap(), "supported_versions")?;

            let group = selected_group(args);
            let key_share = selected_key_share(args, group)?;
            vec![server_hello_record(
                &hello.session_id,
                cipher,
                server_random()?,
                vec![
                    extension(0x002b, &u16_bytes(version)),
                    extension(0x0033, &server_key_share_payload(group, &key_share)),
                ],
                true,
            )?]
        }
        Probe::Tls13Alpn => {
            let cipher = selected_tls13_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));
            let group = selected_group(args);
            let key_share = selected_key_share(args, group)?;

            let protocol = args
                .alpn
                .clone()
                .or_else(|| first_alpn(hello))
                .unwrap_or_else(|| "jj".to_string());
            selected.push(SelectedOutput {
                name: "alpn",
                value: protocol.clone(),
            });
            offered.push(check_alpn(&protocol, &hello.alpn_protocols));
            ensure_offered(args, offered.last().unwrap(), "alpn_protocols")?;

            vec![server_hello_record(
                &hello.session_id,
                cipher,
                server_random()?,
                vec![
                    extension(0x002b, &u16_bytes(TLS13)),
                    extension(0x0033, &server_key_share_payload(group, &key_share)),
                    extension(0x0010, &alpn_payload(protocol.as_bytes())?),
                ],
                true,
            )?]
        }
        Probe::Tls13UnsolicitedExtension => {
            let cipher = selected_tls13_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));
            let group = selected_group(args);
            let key_share = selected_key_share(args, group)?;
            let extension_type = args
                .extension
                .unwrap_or(DEFAULT_EXTENSION);
            let payload = parse_hex_bytes(&args.extension_payload_hex)?;

            selected.push(selected_u16("extension", extension_type));
            offered.push(check_u16(
                "extensions",
                extension_type,
                &hello.extension_types(),
            ));
            if offered
                .last()
                .is_some_and(|check| check.offered)
            {
                notes.push("The chosen extension was actually offered by the client; pick a different --extension for an unsolicited-extension probe.".to_string());
            }

            vec![server_hello_record(
                &hello.session_id,
                cipher,
                server_random()?,
                vec![
                    extension(0x002b, &u16_bytes(TLS13)),
                    extension(0x0033, &server_key_share_payload(group, &key_share)),
                    extension(extension_type, &payload),
                ],
                true,
            )?]
        }
        Probe::Tls12Cipher => {
            let cipher = selected_tls12_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));
            offered.push(check_u16("cipher_suites", cipher, &hello.cipher_suites));
            ensure_offered(args, offered.last().unwrap(), "cipher_suites")?;
            selected.push(SelectedOutput {
                name: "session_id",
                value: tls12_session_id_mode(args),
            });

            notes.push(
                "This sends only ServerHello; a client that accepts the selection will usually wait for Certificate/ServerKeyExchange."
                    .to_string(),
            );
            vec![server_hello_record(
                tls12_session_id(args, hello),
                cipher,
                server_random()?,
                Vec::new(),
                false,
            )?]
        }
        Probe::Tls12SignatureScheme => {
            let signature_scheme = args.signature_scheme;
            selected.push(selected_u16("signature_scheme", signature_scheme));
            offered.push(check_u16(
                "signature_schemes",
                signature_scheme,
                &hello.signature_schemes,
            ));
            ensure_offered(args, offered.last().unwrap(), "signature_schemes")?;

            let cipher = selected_tls12_signature_cipher(args, signature_scheme, hello);
            selected.push(selected_u16("cipher", cipher));
            offered.push(check_u16("cipher_suites", cipher, &hello.cipher_suites));
            ensure_offered(args, offered.last().unwrap(), "cipher_suites")?;

            let group = args
                .group
                .unwrap_or(DEFAULT_TLS12_ECDHE_GROUP);
            selected.push(selected_u16("group", group));
            offered.push(check_u16(
                "supported_groups",
                group,
                &hello.supported_groups,
            ));
            ensure_offered(args, offered.last().unwrap(), "supported_groups")?;

            selected.push(SelectedOutput {
                name: "cert_chain",
                value: args.cert_chain.as_str().to_string(),
            });
            selected.push(SelectedOutput {
                name: "ske_signature",
                value: if args.ske_signature_hex.is_some() {
                    "custom".to_string()
                } else {
                    "intentionally-bad".to_string()
                },
            });

            notes.push(
                "If the certificate chain is not trusted, many clients will stop with unknown_ca before this tests ServerKeyExchange signature handling."
                    .to_string(),
            );
            notes.push(
                "With a trusted test CA, unsupported schemes usually produce illegal_parameter; accepted schemes with this bad signature usually produce decrypt_error."
                    .to_string(),
            );

            let key_share = selected_key_share(args, group)?;
            let signature = selected_ske_signature(args, signature_scheme)?;
            vec![
                server_hello_record(
                    tls12_session_id(args, hello),
                    cipher,
                    server_random()?,
                    Vec::new(),
                    false,
                )?,
                certificate_record(args.cert_chain.chain())?,
                server_key_exchange_record(signature_scheme, group, &key_share, &signature)?,
                handshake_record(14, &[])?,
            ]
        }
        Probe::Tls12UnsolicitedExtension => {
            let cipher = selected_tls12_cipher(args, hello);
            selected.push(selected_u16("cipher", cipher));
            let extension_type = args
                .extension
                .unwrap_or(DEFAULT_EXTENSION);
            let payload = parse_hex_bytes(&args.extension_payload_hex)?;
            selected.push(selected_u16("extension", extension_type));
            selected.push(SelectedOutput {
                name: "session_id",
                value: tls12_session_id_mode(args),
            });
            offered.push(check_u16(
                "extensions",
                extension_type,
                &hello.extension_types(),
            ));

            vec![server_hello_record(
                tls12_session_id(args, hello),
                cipher,
                server_random()?,
                vec![extension(extension_type, &payload)],
                true,
            )?]
        }
        Probe::Alert => {
            selected.push(SelectedOutput {
                name: "alert_description",
                value: format!(
                    "{} ({})",
                    args.alert_description,
                    alert_description_name(args.alert_description)
                ),
            });
            vec![alert_record(2, args.alert_description)]
        }
    };

    let sent_records = records
        .iter()
        .map(|record| summarize_record(record))
        .collect::<Vec<_>>();
    for record in &records {
        stream
            .write_all(record)
            .context("failed to write probe TLS record")?;
    }
    stream
        .flush()
        .context("failed to flush probe TLS record")?;

    let reaction = read_reaction(stream, args.max_reaction_records)?;

    Ok(ProbeOutput {
        name: args.probe.as_str(),
        selected,
        offered,
        sent_records,
        reaction,
        notes,
    })
}

fn selected_tls13_cipher(args: &Args, hello: &ClientHello) -> u16 {
    args.cipher
        .or_else(|| {
            hello
                .cipher_suites
                .iter()
                .copied()
                .find(|suite| matches!(*suite, 0x1301 | 0x1302 | 0x1303))
        })
        .unwrap_or(DEFAULT_TLS13_CIPHER)
}

fn selected_tls12_cipher(args: &Args, hello: &ClientHello) -> u16 {
    args.cipher
        .or_else(|| {
            hello
                .cipher_suites
                .iter()
                .copied()
                .find(|suite| !is_grease(*suite) && !matches!(*suite, 0x1301 | 0x1302 | 0x1303))
        })
        .unwrap_or(DEFAULT_TLS12_CIPHER)
}

fn selected_tls12_signature_cipher(args: &Args, signature_scheme: u16, hello: &ClientHello) -> u16 {
    if let Some(cipher) = args.cipher {
        return cipher;
    }

    let preferred = if signature_scheme_algorithm(signature_scheme) == Some("ecdsa") {
        DEFAULT_TLS12_ECDSA_CIPHER
    } else {
        DEFAULT_TLS12_CIPHER
    };
    if hello.cipher_suites.contains(&preferred) {
        preferred
    } else {
        selected_tls12_cipher(args, hello)
    }
}

fn selected_group(args: &Args) -> u16 {
    args.group.unwrap_or(DEFAULT_GROUP)
}

fn selected_key_share(args: &Args, group: u16) -> Result<Vec<u8>> {
    if let Some(hex) = &args.key_share_hex {
        return parse_hex_bytes(hex);
    }

    let len = args
        .key_share_len
        .unwrap_or_else(|| default_key_share_len(group));
    if group == 0x0017 && len == 65 {
        return p256_public_key();
    }

    if group == 0x001d && len == 32 {
        let mut key = vec![0u8; 32];
        key[0] = 9;
        return Ok(key);
    }

    let mut key = vec![0u8; len];
    if len == 0 {
        return Ok(key);
    }
    SystemRandom::new()
        .fill(&mut key)
        .map_err(|_| anyhow!("failed to generate synthetic key share bytes"))?;

    if matches!(group, 0x0017 | 0x0018 | 0x0019) && !key.is_empty() {
        key[0] = 4;
    }

    Ok(key)
}

fn p256_public_key() -> Result<Vec<u8>> {
    let private_key =
        agreement::EphemeralPrivateKey::generate(&agreement::ECDH_P256, &SystemRandom::new())
            .map_err(|_| anyhow!("failed to generate P-256 ServerKeyExchange key"))?;
    let public_key = private_key
        .compute_public_key()
        .map_err(|_| anyhow!("failed to compute P-256 ServerKeyExchange public key"))?;
    Ok(public_key.as_ref().to_vec())
}

fn default_key_share_len(group: u16) -> usize {
    match group {
        0x001d => 32,
        0x0017 => 65,
        0x0018 => 97,
        0x0019 => 133,
        0x0100 => 256,
        0x0101 => 384,
        0x0102 => 512,
        0x0103 => 768,
        0x0104 => 1024,
        0x11ec => 1216,
        group if is_grease(group) => 1,
        _ => 32,
    }
}

fn ensure_offered(args: &Args, check: &OfferCheckOutput, field: &str) -> Result<()> {
    if args.require_offered && !check.offered {
        bail!(
            "--require-offered set, but {} did not contain {}",
            field,
            check.value
        );
    }
    Ok(())
}

fn first_alpn(hello: &ClientHello) -> Option<String> {
    hello
        .alpn_protocols
        .first()
        .map(|protocol| String::from_utf8_lossy(protocol).into_owned())
}

fn tls12_session_id<'a>(args: &Args, hello: &'a ClientHello) -> &'a [u8] {
    if args.tls12_echo_session_id {
        &hello.session_id
    } else {
        &[]
    }
}

fn tls12_session_id_mode(args: &Args) -> String {
    if args.tls12_echo_session_id {
        "echo-client-session-id".to_string()
    } else {
        "empty".to_string()
    }
}

fn server_hello_record(
    session_id: &[u8],
    cipher: u16,
    random: [u8; 32],
    extensions: Vec<Vec<u8>>,
    include_extensions: bool,
) -> Result<Vec<u8>> {
    if session_id.len() > u8::MAX as usize {
        bail!("cannot echo overlong ClientHello session id");
    }

    let mut body = Vec::new();
    put_u16(&mut body, TLS12);
    body.extend_from_slice(&random);
    body.push(session_id.len() as u8);
    body.extend_from_slice(session_id);
    put_u16(&mut body, cipher);
    body.push(0);

    if include_extensions || !extensions.is_empty() {
        let extension_block = concat_extensions(extensions)?;
        put_u16(
            &mut body,
            checked_u16_len(extension_block.len(), "ServerHello extensions")?,
        );
        body.extend_from_slice(&extension_block);
    }

    Ok(handshake_record(2, &body)?)
}

fn tls13_server_hello_record(
    session_id: &[u8],
    cipher: u16,
    group: u16,
    key_share: &[u8],
) -> Result<Vec<u8>> {
    server_hello_record(
        session_id,
        cipher,
        server_random()?,
        vec![
            extension(0x002b, &u16_bytes(TLS13)),
            extension(0x0033, &server_key_share_payload(group, key_share)),
        ],
        true,
    )
}

fn server_random() -> Result<[u8; 32]> {
    let mut random = [0u8; 32];
    SystemRandom::new()
        .fill(&mut random)
        .map_err(|_| anyhow!("failed to generate ServerHello random"))?;
    Ok(random)
}

fn server_key_share_payload(group: u16, key_share: &[u8]) -> Vec<u8> {
    let mut payload = Vec::new();
    put_u16(&mut payload, group);
    put_u16(&mut payload, key_share.len() as u16);
    payload.extend_from_slice(key_share);
    payload
}

fn certificate_record(chain: &[&[u8]]) -> Result<Vec<u8>> {
    let mut list = Vec::new();
    for cert in chain {
        put_u24(&mut list, cert.len())?;
        list.extend_from_slice(cert);
    }

    let mut body = Vec::new();
    put_u24(&mut body, list.len())?;
    body.extend_from_slice(&list);
    handshake_record(11, &body)
}

fn server_key_exchange_record(
    signature_scheme: u16,
    group: u16,
    key_share: &[u8],
    signature: &[u8],
) -> Result<Vec<u8>> {
    if key_share.len() > u8::MAX as usize {
        bail!("TLS 1.2 ECDHE point is too long for ServerKeyExchange");
    }

    let mut body = Vec::new();
    body.push(3);
    put_u16(&mut body, group);
    body.push(key_share.len() as u8);
    body.extend_from_slice(key_share);
    put_u16(&mut body, signature_scheme);
    put_u16(
        &mut body,
        checked_u16_len(signature.len(), "ServerKeyExchange signature")?,
    );
    body.extend_from_slice(signature);
    handshake_record(12, &body)
}

fn selected_ske_signature(args: &Args, signature_scheme: u16) -> Result<Vec<u8>> {
    if let Some(hex) = &args.ske_signature_hex {
        return parse_hex_bytes(hex);
    }
    Ok(dummy_signature(signature_scheme))
}

fn dummy_signature(signature_scheme: u16) -> Vec<u8> {
    match signature_scheme_algorithm(signature_scheme) {
        Some("ecdsa") => vec![0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01],
        Some("rsa") => vec![0xa5; 256],
        Some("ed25519") => vec![0xa5; 64],
        Some("ed448") => vec![0xa5; 114],
        _ => vec![0xa5; 64],
    }
}

fn signature_scheme_algorithm(signature_scheme: u16) -> Option<&'static str> {
    match signature_scheme {
        0x0201 | 0x0401 | 0x0501 | 0x0601 | 0x0804 | 0x0805 | 0x0806 => Some("rsa"),
        0x0203 | 0x0403 | 0x0503 | 0x0603 => Some("ecdsa"),
        0x0807 => Some("ed25519"),
        0x0808 => Some("ed448"),
        _ => None,
    }
}

fn alpn_payload(protocol: &[u8]) -> Result<Vec<u8>> {
    if protocol.len() > u8::MAX as usize {
        bail!("ALPN protocol is too long");
    }
    let list_len = protocol.len() + 1;
    let mut payload = Vec::new();
    put_u16(&mut payload, checked_u16_len(list_len, "ALPN list")?);
    payload.push(protocol.len() as u8);
    payload.extend_from_slice(protocol);
    Ok(payload)
}

fn extension(extension_type: u16, payload: &[u8]) -> Vec<u8> {
    let mut out = Vec::new();
    put_u16(&mut out, extension_type);
    put_u16(&mut out, payload.len() as u16);
    out.extend_from_slice(payload);
    out
}

fn concat_extensions(extensions: Vec<Vec<u8>>) -> Result<Vec<u8>> {
    let len = extensions
        .iter()
        .map(Vec::len)
        .sum::<usize>();
    checked_u16_len(len, "ServerHello extensions")?;
    let mut out = Vec::with_capacity(len);
    for extension in extensions {
        out.extend_from_slice(&extension);
    }
    Ok(out)
}

fn handshake_record(handshake_type: u8, body: &[u8]) -> Result<Vec<u8>> {
    let mut handshake = Vec::new();
    handshake.push(handshake_type);
    put_u24(&mut handshake, body.len())?;
    handshake.extend_from_slice(body);
    tls_record(TLS_RECORD_HANDSHAKE, &handshake)
}

fn alert_record(level: u8, description: u8) -> Vec<u8> {
    tls_record(TLS_RECORD_ALERT, &[level, description]).expect("alert record fits in u16")
}

fn tls_record(content_type: u8, payload: &[u8]) -> Result<Vec<u8>> {
    let mut record = Vec::new();
    record.push(content_type);
    put_u16(&mut record, TLS12);
    put_u16(&mut record, checked_u16_len(payload.len(), "TLS record")?);
    record.extend_from_slice(payload);
    Ok(record)
}

fn read_reaction(stream: &mut TcpStream, max_records: usize) -> Result<ReactionOutput> {
    let mut records = Vec::new();

    for _ in 0..max_records {
        match read_tls_record(stream) {
            Ok(Some(record)) => {
                let summary = summarize_record(&record);
                let terminal = terminal_for_record(&record);
                records.push(summary);
                if let Some(terminal) = terminal {
                    return Ok(ReactionOutput { terminal, records });
                }
            }
            Ok(None) => {
                return Ok(ReactionOutput {
                    terminal: "closed".to_string(),
                    records,
                });
            }
            Err(err) if is_timeout(&err) => {
                return Ok(ReactionOutput {
                    terminal: "timeout".to_string(),
                    records,
                });
            }
            Err(err) => {
                return Ok(ReactionOutput {
                    terminal: format!("read_error: {err}"),
                    records,
                });
            }
        }
    }

    Ok(ReactionOutput {
        terminal: "record_limit_reached".to_string(),
        records,
    })
}

fn read_tls_record(stream: &mut TcpStream) -> io::Result<Option<Vec<u8>>> {
    let mut header = [0u8; 5];
    match stream.read_exact(&mut header) {
        Ok(()) => {}
        Err(err) if err.kind() == io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(err) => return Err(err),
    }

    let len = u16::from_be_bytes([header[3], header[4]]) as usize;
    let mut payload = vec![0u8; len];
    match stream.read_exact(&mut payload) {
        Ok(()) => {}
        Err(err) if err.kind() == io::ErrorKind::UnexpectedEof => return Ok(None),
        Err(err) => return Err(err),
    }

    let mut record = header.to_vec();
    record.extend_from_slice(&payload);
    Ok(Some(record))
}

fn summarize_record(record: &[u8]) -> RecordOutput {
    if record.len() < 5 {
        return RecordOutput {
            content_type: "truncated".to_string(),
            legacy_version: "unknown".to_string(),
            length: record.len(),
            summary: "truncated TLS record".to_string(),
        };
    }

    let content_type = record[0];
    let version = u16::from_be_bytes([record[1], record[2]]);
    let len = u16::from_be_bytes([record[3], record[4]]) as usize;
    let payload = record.get(5..).unwrap_or_default();
    let summary = match content_type {
        20 => "change_cipher_spec".to_string(),
        21 => summarize_alert(payload),
        22 => summarize_handshake(payload),
        23 => "application_data_or_encrypted_handshake".to_string(),
        _ => format!("unknown content type {content_type}"),
    };

    RecordOutput {
        content_type: format!("0x{content_type:02x} ({})", content_type_name(content_type)),
        legacy_version: hex_u16(version),
        length: len,
        summary,
    }
}

fn terminal_for_record(record: &[u8]) -> Option<String> {
    let content_type = *record.first()?;
    let payload = record.get(5..).unwrap_or_default();
    match content_type {
        TLS_RECORD_ALERT => Some(summarize_alert(payload)),
        TLS_RECORD_APPLICATION_DATA => {
            Some("client_continued_with_encrypted_handshake".to_string())
        }
        TLS_RECORD_HANDSHAKE if payload.first().copied() == Some(1) => {
            Some("client_sent_second_client_hello".to_string())
        }
        _ => None,
    }
}

fn summarize_alert(payload: &[u8]) -> String {
    if payload.len() < 2 {
        return format!("alert: malformed {} bytes", payload.len());
    }
    format!(
        "alert: level {} ({}), description {} ({})",
        payload[0],
        alert_level_name(payload[0]),
        payload[1],
        alert_description_name(payload[1])
    )
}

fn summarize_handshake(payload: &[u8]) -> String {
    let mut offset = 0;
    let mut types = Vec::new();
    while offset + 4 <= payload.len() {
        let handshake_type = payload[offset];
        let len = ((payload[offset + 1] as usize) << 16)
            | ((payload[offset + 2] as usize) << 8)
            | payload[offset + 3] as usize;
        types.push(format!(
            "0x{handshake_type:02x} ({})",
            handshake_type_name(handshake_type)
        ));
        offset += 4 + len;
    }
    if types.is_empty() {
        "handshake: empty_or_encrypted".to_string()
    } else {
        format!("handshake: {}", types.join(", "))
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
    if header[0] != TLS_RECORD_HANDSHAKE {
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

fn parse_client_hello_body(body: &[u8]) -> Result<ClientHello> {
    let mut offset = 0;
    let legacy_version = take_u16(body, &mut offset)?;
    take(body, &mut offset, 32)?;

    let session_id_len = take_u8(body, &mut offset)? as usize;
    let session_id = take(body, &mut offset, session_id_len)?.to_vec();

    let cipher_suites_len = take_u16(body, &mut offset)? as usize;
    if cipher_suites_len % 2 != 0 {
        bail!("cipher_suites vector has an odd length");
    }
    let cipher_suites = take(body, &mut offset, cipher_suites_len)?
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect::<Vec<_>>();

    let compression_methods_len = take_u8(body, &mut offset)? as usize;
    take(body, &mut offset, compression_methods_len)?;

    let mut hello = ClientHello {
        legacy_version,
        session_id,
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
        let payload = take(body, &mut offset, extension_len)?.to_vec();

        match extension_type {
            0x0000 => hello.server_name = parse_sni(&payload),
            0x000a => hello.supported_groups = parse_u16_vector(&payload, 2).unwrap_or_default(),
            0x000d => hello.signature_schemes = parse_u16_vector(&payload, 2).unwrap_or_default(),
            0x0010 => hello.alpn_protocols = parse_alpn(&payload).unwrap_or_default(),
            0x002b => {
                hello.supported_versions = parse_supported_versions(&payload).unwrap_or_default();
            }
            0x0033 => {
                hello.key_share_groups =
                    parse_client_key_share_groups(&payload).unwrap_or_default();
            }
            _ => {}
        }

        hello.extensions.push(ClientExtension {
            extension_type,
            payload,
        });
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
    Ok(take(extension, &mut offset, versions_len)?
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect())
}

fn parse_client_key_share_groups(extension: &[u8]) -> Result<Vec<u16>> {
    let mut offset = 0;
    let shares_len = take_u16(extension, &mut offset)? as usize;
    let shares_end = offset
        .checked_add(shares_len)
        .ok_or_else(|| anyhow!("key_share list length overflow"))?;
    if shares_end > extension.len() {
        bail!("key_share list length exceeds extension length");
    }

    let mut groups = Vec::new();
    while offset < shares_end {
        let group = take_u16(extension, &mut offset)?;
        let key_len = take_u16(extension, &mut offset)? as usize;
        take(extension, &mut offset, key_len)?;
        groups.push(group);
    }
    Ok(groups)
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
    Ok(take(extension, &mut offset, item_bytes_len)?
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect())
}

impl ClientHello {
    fn extension_types(&self) -> Vec<u16> {
        self.extensions
            .iter()
            .map(|extension| {
                let _ = extension.payload.len();
                extension.extension_type
            })
            .collect()
    }
}

impl From<&ClientHello> for ClientHelloOutput {
    fn from(hello: &ClientHello) -> Self {
        Self {
            legacy_version: hex_u16(hello.legacy_version),
            server_name: hello.server_name.clone(),
            alpn_protocols: hello
                .alpn_protocols
                .iter()
                .map(|protocol| String::from_utf8_lossy(protocol).into_owned())
                .collect(),
            supported_versions: hex_u16_list(&hello.supported_versions),
            cipher_suites: hex_u16_list(&hello.cipher_suites),
            extensions: hex_u16_list(&hello.extension_types()),
            supported_groups: hex_u16_list(&hello.supported_groups),
            key_share_groups: hex_u16_list(&hello.key_share_groups),
            signature_schemes: hex_u16_list(&hello.signature_schemes),
            handshake_bytes: hello.handshake_len,
        }
    }
}

fn selected_u16(name: &'static str, value: u16) -> SelectedOutput {
    SelectedOutput {
        name,
        value: hex_u16(value),
    }
}

fn check_u16(name: &'static str, value: u16, offered_values: &[u16]) -> OfferCheckOutput {
    OfferCheckOutput {
        name,
        value: hex_u16(value),
        offered: offered_values.contains(&value),
    }
}

fn check_alpn(protocol: &str, offered_protocols: &[Vec<u8>]) -> OfferCheckOutput {
    OfferCheckOutput {
        name: "alpn_protocols",
        value: protocol.to_string(),
        offered: offered_protocols
            .iter()
            .any(|offered| offered.as_slice() == protocol.as_bytes()),
    }
}

fn parse_u16_arg(input: &str) -> Result<u16, String> {
    let input = input.trim();
    let without_prefix = input
        .strip_prefix("0x")
        .or_else(|| input.strip_prefix("0X"));
    match without_prefix {
        Some(hex) => u16::from_str_radix(hex, 16).map_err(|err| err.to_string()),
        None => input
            .parse::<u16>()
            .map_err(|err| err.to_string()),
    }
}

fn parse_u8_arg(input: &str) -> Result<u8, String> {
    let input = input.trim();
    let without_prefix = input
        .strip_prefix("0x")
        .or_else(|| input.strip_prefix("0X"));
    match without_prefix {
        Some(hex) => u8::from_str_radix(hex, 16).map_err(|err| err.to_string()),
        None => input
            .parse::<u8>()
            .map_err(|err| err.to_string()),
    }
}

fn parse_hex_bytes(input: &str) -> Result<Vec<u8>> {
    let input = input.trim();
    let input = input
        .strip_prefix("0x")
        .or_else(|| input.strip_prefix("0X"))
        .unwrap_or(input);
    if input.is_empty() {
        return Ok(Vec::new());
    }
    if input.len() % 2 != 0 {
        bail!("hex string has odd length");
    }

    input
        .as_bytes()
        .chunks_exact(2)
        .map(|chunk| {
            let hi = hex_nibble(chunk[0])?;
            let lo = hex_nibble(chunk[1])?;
            Ok((hi << 4) | lo)
        })
        .collect()
}

fn hex_nibble(byte: u8) -> Result<u8> {
    match byte {
        b'0'..=b'9' => Ok(byte - b'0'),
        b'a'..=b'f' => Ok(byte - b'a' + 10),
        b'A'..=b'F' => Ok(byte - b'A' + 10),
        _ => bail!("invalid hex digit {}", byte as char),
    }
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
        bail!("unexpected end of ClientHello");
    }
    Ok(((bytes[offset] as usize) << 16)
        | ((bytes[offset + 1] as usize) << 8)
        | bytes[offset + 2] as usize)
}

fn put_u16(out: &mut Vec<u8>, value: u16) {
    out.extend_from_slice(&value.to_be_bytes());
}

fn put_u24(out: &mut Vec<u8>, value: usize) -> Result<()> {
    if value > 0x00ff_ffff {
        bail!("value does not fit in u24: {value}");
    }
    out.push(((value >> 16) & 0xff) as u8);
    out.push(((value >> 8) & 0xff) as u8);
    out.push((value & 0xff) as u8);
    Ok(())
}

fn checked_u16_len(len: usize, field: &str) -> Result<u16> {
    u16::try_from(len).with_context(|| format!("{field} length exceeds u16: {len}"))
}

fn u16_bytes(value: u16) -> Vec<u8> {
    value.to_be_bytes().to_vec()
}

fn hex_u16(value: u16) -> String {
    format!("0x{value:04x}")
}

fn hex_u16_list(values: &[u16]) -> Vec<String> {
    values
        .iter()
        .map(|value| hex_u16(*value))
        .collect()
}

fn is_grease(value: u16) -> bool {
    value & 0x000f == 0x000a && value >> 8 == value & 0x00ff
}

fn is_timeout(err: &io::Error) -> bool {
    matches!(
        err.kind(),
        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock
    )
}

fn content_type_name(value: u8) -> &'static str {
    match value {
        20 => "change_cipher_spec",
        21 => "alert",
        22 => "handshake",
        23 => "application_data",
        24 => "heartbeat",
        _ => "unknown",
    }
}

fn handshake_type_name(value: u8) -> &'static str {
    match value {
        1 => "client_hello",
        2 => "server_hello",
        4 => "new_session_ticket",
        8 => "encrypted_extensions",
        11 => "certificate",
        13 => "certificate_request",
        14 => "server_hello_done",
        15 => "certificate_verify",
        16 => "client_key_exchange",
        20 => "finished",
        24 => "key_update",
        _ => "unknown",
    }
}

fn alert_level_name(value: u8) -> &'static str {
    match value {
        1 => "warning",
        2 => "fatal",
        _ => "unknown",
    }
}

fn alert_description_name(value: u8) -> &'static str {
    match value {
        0 => "close_notify",
        10 => "unexpected_message",
        20 => "bad_record_mac",
        21 => "decryption_failed_RESERVED",
        22 => "record_overflow",
        30 => "decompression_failure_RESERVED",
        40 => "handshake_failure",
        42 => "bad_certificate",
        43 => "unsupported_certificate",
        44 => "certificate_revoked",
        45 => "certificate_expired",
        46 => "certificate_unknown",
        47 => "illegal_parameter",
        48 => "unknown_ca",
        49 => "access_denied",
        50 => "decode_error",
        51 => "decrypt_error",
        70 => "protocol_version",
        71 => "insufficient_security",
        80 => "internal_error",
        86 => "inappropriate_fallback",
        90 => "user_canceled",
        109 => "missing_extension",
        110 => "unsupported_extension",
        112 => "unrecognized_name",
        115 => "unknown_psk_identity",
        116 => "certificate_required",
        120 => "no_application_protocol",
        _ => "unknown",
    }
}
