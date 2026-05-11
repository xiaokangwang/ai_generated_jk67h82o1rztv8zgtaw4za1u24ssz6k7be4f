use std::fs;
use std::io::{ErrorKind, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Output, Stdio};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use rustls::craft::{
    self, ApplicationSettingsCodepoint, CraftExtension, ExtensionSpec, Fingerprint, GreaseOr,
    GreaseOrProtocol, GreaseOrPskKeyExchangeMode, KeepExtension,
};
use rustls::enums::{CertificateCompressionAlgorithm, CertificateType};
use rustls::pki_types::{CertificateDer, ServerName};
use rustls::{
    CipherSuite, ClientConfig, ExtensionType, NamedGroup, ProtocolVersion, RootCertStore,
    SignatureScheme,
};
use rustls_util::{Stream, complete_io};

#[test]
#[ignore = "requires built NSS selfserv/certutil/pk12util; set NSS_SELFSERV, NSS_CERTUTIL, and NSS_PK12UTIL"]
fn craftls_client_all_extensions_connects_to_nss_server() {
    let tools = NssTools::find();
    let db = NssDb::new(&tools);
    let mut server = spawn_nss_server(
        &tools,
        &db,
        &NssServerConfig {
            version: ProtocolVersion::TLSv1_3,
            group: NamedGroup::X25519,
            alpn: true,
            tickets: true,
            early_data: true,
            no_cache: false,
            cert_compression: true,
        },
    );

    let config = craft_client_config(all_extensions_fingerprint(), true);
    let first = run_craft_client_to_nss(
        "nss all extensions/full",
        server.port,
        config.clone(),
        ProtocolVersion::TLSv1_3,
        Some(HTTP1),
        false,
    );
    let second = run_craft_client_to_nss(
        "nss all extensions/resume",
        server.port,
        config,
        ProtocolVersion::TLSv1_3,
        Some(HTTP1),
        false,
    );
    let output = stop_nss_server(&mut server, ProtocolVersion::TLSv1_3, Some(HTTP1));
    assert!(
        output.status.success(),
        "NSS selfserv failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert_client_hello_covers_all_extensions(&[first, second]);
}

#[test]
#[ignore = "requires built NSS selfserv/certutil/pk12util; set NSS_SELFSERV, NSS_CERTUTIL, and NSS_PK12UTIL"]
fn craftls_client_nss_extension_configuration_matrix() {
    let tools = NssTools::find();
    let db = NssDb::new(&tools);
    let cases = extension_matrix_cases();
    assert_eq!(cases.len(), 5120);
    for case in cases {
        run_matrix_case(&tools, &db, &case);
    }
}

fn craft_client_config(fingerprint: Fingerprint, enable_early_data: bool) -> Arc<ClientConfig> {
    let mut config = ClientConfig::builder(rustls_aws_lc_rs::DEFAULT_PROVIDER.into())
        .with_root_certificates(root_ca())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(fingerprint.builder());
    config.enable_early_data = enable_early_data;
    Arc::new(config)
}

fn run_craft_client_to_nss(
    case_name: &str,
    port: u16,
    config: Arc<ClientConfig>,
    expected_version: ProtocolVersion,
    expected_alpn: Option<&[u8]>,
    stop: bool,
) -> Vec<u8> {
    let mut io = RecordingStream::new(connect_to_nss(port));
    let server_name = ServerName::try_from("localhost")
        .unwrap()
        .to_owned();
    let mut client = config
        .connect(server_name)
        .build()
        .unwrap();

    complete_io(&mut io, &mut client)
        .unwrap_or_else(|err| panic!("{case_name}: handshake failed: {err:?}"));
    assert_eq!(
        client.protocol_version(),
        Some(expected_version),
        "{case_name}: protocol version mismatch"
    );
    assert_eq!(
        client
            .alpn_protocol()
            .map(|protocol| protocol.as_ref()),
        expected_alpn,
        "{case_name}: ALPN mismatch"
    );

    let client_hello = first_client_hello_record(&io.writes);
    let request = if stop {
        b"GET /stop HTTP/1.0\r\n\r\n".as_slice()
    } else {
        b"GET / HTTP/1.0\r\n\r\n".as_slice()
    };
    let response = read_http_response(case_name, &mut client, &mut io, request);
    assert!(
        response.starts_with(b"HTTP/1.0 200 OK"),
        "{case_name}: bad NSS response: {}",
        String::from_utf8_lossy(&response)
    );

    client_hello
}

fn read_http_response(
    case_name: &str,
    client: &mut rustls::ClientConnection,
    io: &mut RecordingStream,
    request: &[u8],
) -> Vec<u8> {
    let mut tls = Stream::new(client, io);
    tls.write_all(request)
        .unwrap_or_else(|err| panic!("{case_name}: failed to write HTTP request: {err:?}"));

    let mut response = Vec::new();
    let mut buf = [0u8; 1024];
    loop {
        match tls.read(&mut buf) {
            Ok(0) => break,
            Ok(read) => {
                response.extend_from_slice(&buf[..read]);
                if response
                    .windows(NSS_EOF_MARKER.len())
                    .any(|window| window == NSS_EOF_MARKER)
                {
                    break;
                }
            }
            Err(_err) if !response.is_empty() => break,
            Err(err) => panic!("{case_name}: failed to read HTTP response: {err:?}"),
        }
    }
    response
}

fn spawn_nss_server(tools: &NssTools, db: &NssDb, config: &NssServerConfig) -> NssServer {
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    let port = listener.local_addr().unwrap().port();
    drop(listener);

    let mut command = Command::new(&tools.selfserv);
    tools.configure_runtime(&mut command);
    command
        .arg("-D")
        .arg("-p")
        .arg(port.to_string())
        .arg("-d")
        .arg(db.dir_arg())
        .arg("-n")
        .arg(SERVER_NICKNAME)
        .arg("-w")
        .arg("")
        .arg("-V")
        .arg(nss_version_range(config.version))
        .arg("-I")
        .arg(nss_group_name(config.group))
        .arg("-G")
        .arg("-H")
        .arg("0")
        .arg("-t")
        .arg("1");

    if config.alpn {
        command.arg("-Q");
    }
    if config.tickets {
        command.arg("-u");
    }
    if config.early_data {
        command.arg("-Z");
    }
    if config.no_cache {
        command.arg("-N");
    }
    if config.cert_compression {
        command.arg("-q");
    }

    let child = command
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap_or_else(|err| panic!("failed to start NSS selfserv {:?}: {err}", tools.selfserv));

    let mut server = NssServer {
        child: Some(child),
        port,
    };
    wait_for_nss_listener(&mut server);
    server
}

fn wait_for_nss_listener(server: &mut NssServer) {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match TcpStream::connect(("127.0.0.1", server.port)) {
            Ok(_) => return,
            Err(err)
                if matches!(
                    err.kind(),
                    ErrorKind::ConnectionRefused
                        | ErrorKind::TimedOut
                        | ErrorKind::AddrNotAvailable
                ) =>
            {
                if let Some(output) = server.exited_output() {
                    panic!(
                        "NSS selfserv exited before listening: {}\nstdout:\n{}\nstderr:\n{}",
                        output.status,
                        String::from_utf8_lossy(&output.stdout),
                        String::from_utf8_lossy(&output.stderr)
                    );
                }
                assert!(
                    Instant::now() <= deadline,
                    "timed out waiting for NSS selfserv"
                );
                thread::sleep(Duration::from_millis(10));
            }
            Err(err) => panic!("failed to poll NSS selfserv listener: {err}"),
        }
    }
}

fn connect_to_nss(port: u16) -> TcpStream {
    let stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    stream
        .set_write_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    stream
}

fn stop_nss_server(
    server: &mut NssServer,
    expected_version: ProtocolVersion,
    expected_alpn: Option<&[u8]>,
) -> Output {
    let config = craft_client_config(
        minimal_fingerprint(expected_alpn.is_some(), AlpsMode::None),
        false,
    );
    let _ = run_craft_client_to_nss(
        "nss stop",
        server.port,
        config,
        expected_version,
        expected_alpn,
        true,
    );
    server.wait_with_output()
}

fn extension_matrix_cases() -> Vec<MatrixCase> {
    let mut cases = Vec::new();

    for bits in 0..16 {
        let extension_groups = ExtensionGroups(bits);
        for version in [ProtocolVersion::TLSv1_2, ProtocolVersion::TLSv1_3] {
            for group in [NamedGroup::X25519, NamedGroup::secp256r1] {
                for server_alpn in [false, true] {
                    for client_alpn in [false, true] {
                        for client_alps in
                            [AlpsMode::None, AlpsMode::Old, AlpsMode::New, AlpsMode::Both]
                        {
                            for tickets in [false, true] {
                                for &(server_early_data, client_early_data) in
                                    early_data_combinations(version, tickets)
                                {
                                    cases.push(MatrixCase {
                                        name: format!(
                                            "nss-ext-{bits:04b}-version-{version:?}-group-{group:?}-server-alpn-{server_alpn}-client-alpn-{client_alpn}-alps-{client_alps:?}-tickets-{tickets}-server-early-{server_early_data}-client-early-{client_early_data}"
                                        ),
                                        version,
                                        group,
                                        server_alpn,
                                        client_alpn,
                                        client_alps,
                                        tickets,
                                        server_early_data,
                                        client_early_data,
                                        extension_groups,
                                    });
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    cases
}

fn early_data_combinations(version: ProtocolVersion, tickets: bool) -> &'static [(bool, bool)] {
    if version == ProtocolVersion::TLSv1_3 && tickets {
        &[(false, false), (false, true), (true, false), (true, true)]
    } else {
        &[(false, false), (false, true)]
    }
}

fn run_matrix_case(tools: &NssTools, db: &NssDb, case: &MatrixCase) {
    let mut server = spawn_nss_server(
        tools,
        db,
        &NssServerConfig {
            version: case.version,
            group: case.group,
            alpn: case.server_alpn,
            tickets: case.tickets,
            early_data: case.server_early_data,
            no_cache: !case.tickets,
            cert_compression: case
                .extension_groups
                .has(ExtensionGroups::CERT),
        },
    );

    let config = craft_client_config(matrix_fingerprint(case), case.client_early_data);
    let expected_alpn = (case.server_alpn && case.client_alpn).then_some(HTTP1);
    let first = run_craft_client_to_nss(
        &format!("{}/full", case.name),
        server.port,
        config.clone(),
        case.version,
        expected_alpn,
        false,
    );
    let second = run_craft_client_to_nss(
        &format!("{}/resume", case.name),
        server.port,
        config,
        case.version,
        expected_alpn,
        false,
    );
    let output = stop_nss_server(&mut server, case.version, expected_alpn);
    assert!(
        output.status.success(),
        "{}: NSS selfserv failed\nstdout:\n{}\nstderr:\n{}",
        case.name,
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert_matrix_client_hello_extensions(case, &[first, second]);
}

fn matrix_fingerprint(case: &MatrixCase) -> Fingerprint {
    client_fingerprint(
        case.extension_groups,
        case.client_alpn,
        case.client_alps,
        case.client_early_data,
    )
}

fn all_extensions_fingerprint() -> Fingerprint {
    client_fingerprint(ExtensionGroups::all(), true, AlpsMode::Both, true)
}

fn minimal_fingerprint(client_alpn: bool, client_alps: AlpsMode) -> Fingerprint {
    client_fingerprint(
        ExtensionGroups::COMPAT_GROUP,
        client_alpn,
        client_alps,
        false,
    )
}

fn client_fingerprint(
    groups: ExtensionGroups,
    client_alpn: bool,
    client_alps: AlpsMode,
    client_early_data: bool,
) -> Fingerprint {
    use ExtensionSpec::{Craft, Keep, Rustls};
    use GreaseOr::{Grease, T};
    use KeepExtension::{Must, Optional};

    let mut extensions = Vec::new();
    if groups.has(ExtensionGroups::GREASE) {
        extensions.push(Craft(CraftExtension::Grease1));
    }
    extensions.push(Keep(Must(ExtensionType::ServerName)));

    if groups.has(ExtensionGroups::COMPAT) {
        extensions.push(Rustls(craft::ClientExtension::ExtendedMasterSecretRequest));
        extensions.push(Craft(CraftExtension::RenegotiationInfo));
    }

    extensions.push(Craft(CraftExtension::SupportedCurves(leak_vec(
        greaseable_curves(groups),
    ))));

    if groups.has(ExtensionGroups::COMPAT) {
        extensions.push(Rustls(craft::ClientExtension::EcPointFormats(vec![
            craft::ECPointFormat::Uncompressed,
        ])));
        extensions.push(Keep(Optional(ExtensionType::SessionTicket)));
    }

    if client_alpn {
        if groups.has(ExtensionGroups::GREASE) {
            extensions.push(Craft(CraftExtension::ProtocolsWithGrease(leak_vec(vec![
                GreaseOrProtocol::Protocol(HTTP1),
                GreaseOrProtocol::Grease,
            ]))));
        } else {
            extensions.push(Craft(CraftExtension::Protocols(HTTP1_PROTOCOLS)));
        }
    }

    if groups.has(ExtensionGroups::CERT) {
        extensions.push(Rustls(craft::ClientExtension::CertificateStatusRequest(
            craft::ocsp_req(),
        )));
    }

    extensions.push(Craft(CraftExtension::SignatureAlgorithms(leak_vec(
        greaseable_sigalgs(groups),
    ))));

    if groups.has(ExtensionGroups::CERT) {
        extensions.push(Craft(CraftExtension::SignedCertificateTimestamp));
    }

    let mut key_shares = Vec::new();
    if groups.has(ExtensionGroups::GREASE) {
        key_shares.push(Grease);
    }
    key_shares.push(T(NamedGroup::X25519));
    extensions.push(Craft(CraftExtension::KeyShare(leak_vec(key_shares))));

    let mut psk_modes = vec![GreaseOrPskKeyExchangeMode::T(
        craft::PSKKeyExchangeMode::PSK_DHE_KE,
    )];
    if groups.has(ExtensionGroups::GREASE) {
        psk_modes.push(GreaseOrPskKeyExchangeMode::Grease);
    }
    extensions.push(Craft(CraftExtension::PresharedKeyModes(leak_vec(
        psk_modes,
    ))));

    if client_early_data {
        extensions.push(Keep(Optional(ExtensionType::EarlyData)));
    }

    extensions.push(Craft(CraftExtension::SupportedVersions(leak_vec(
        greaseable_versions(groups),
    ))));

    if groups.has(ExtensionGroups::CERT) {
        extensions.push(Craft(CraftExtension::CompressCert(&[
            CertificateCompressionAlgorithm::Brotli,
        ])));
    }

    match client_alps {
        AlpsMode::None => {}
        AlpsMode::Old => extensions.push(Craft(CraftExtension::ApplicationSettings {
            codepoint: ApplicationSettingsCodepoint::Old,
            protocols: HTTP1_PROTOCOLS,
        })),
        AlpsMode::New => extensions.push(Craft(CraftExtension::ApplicationSettings {
            codepoint: ApplicationSettingsCodepoint::New,
            protocols: HTTP1_PROTOCOLS,
        })),
        AlpsMode::Both => {
            extensions.push(Craft(CraftExtension::ApplicationSettings {
                codepoint: ApplicationSettingsCodepoint::Old,
                protocols: HTTP1_PROTOCOLS,
            }));
            extensions.push(Craft(CraftExtension::ApplicationSettings {
                codepoint: ApplicationSettingsCodepoint::New,
                protocols: HTTP1_PROTOCOLS,
            }));
        }
    }

    if groups.has(ExtensionGroups::GREASE) {
        extensions.push(Craft(CraftExtension::Grease2));
    }

    if groups.has(ExtensionGroups::COMPAT) {
        extensions.push(Craft(CraftExtension::NextProtocolNegotiation));
        extensions.push(Craft(CraftExtension::ChannelId));
    }

    if groups.has(ExtensionGroups::IGNORED) {
        extensions.push(Craft(CraftExtension::UseSrtp {
            profiles: &[0x0001],
            mki: &[],
        }));
    }

    if groups.has(ExtensionGroups::CERT) {
        extensions.push(Craft(CraftExtension::SignatureAlgorithmsCert(leak_vec(
            greaseable_sigalgs_cert(groups),
        ))));
        extensions.push(Craft(CraftExtension::DelegatedCredentials(leak_vec(
            greaseable_delegated_credentials(groups),
        ))));
        extensions.push(Craft(CraftExtension::RecordSizeLimit(0x4001)));
        extensions.push(Craft(CraftExtension::PostHandshakeAuth));
        extensions.push(Craft(CraftExtension::ClientCertificateTypes(&[
            CertificateType::RawPublicKey,
            CertificateType::X509,
        ])));
        extensions.push(Craft(CraftExtension::ServerCertificateTypes(&[
            CertificateType::RawPublicKey,
            CertificateType::X509,
        ])));
        extensions.push(Craft(CraftExtension::CertificateAuthorities(&[
            EMPTY_DER_NAME,
        ])));
        extensions.push(Craft(CraftExtension::TrustAnchors(&[b"ta"])));
    }

    if groups.has(ExtensionGroups::IGNORED) {
        // NSS rejects the IANA QUIC transport parameters codepoint in TCP TLS.
        extensions.push(Craft(CraftExtension::QuicTransportParametersLegacy(&[
            1, 2, 3,
        ])));
        extensions.push(Craft(CraftExtension::Pake(NSS_IGNORED_PAKE_CLIENT_HELLO)));
        extensions.push(Craft(CraftExtension::Raw(ExtensionType(0x1234), &[9])));
    }

    extensions.push(Keep(Optional(ExtensionType::PreSharedKey)));

    Fingerprint {
        extensions: leak_vec(extensions),
        cipher: leak_vec(vec![
            craft::GreaseOrCipher::Grease,
            CipherSuite::TLS13_AES_128_GCM_SHA256.into(),
            CipherSuite::TLS13_AES_256_GCM_SHA384.into(),
            CipherSuite::TLS13_CHACHA20_POLY1305_SHA256.into(),
            CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256.into(),
            CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256.into(),
        ]),
        shuffle_extensions: false,
    }
}

fn assert_client_hello_covers_all_extensions(client_hellos: &[Vec<u8>]) {
    let mut extension_types = Vec::new();
    for client_hello in client_hellos {
        extension_types.extend(client_hello_extension_types(client_hello));
    }

    let expected = [
        ExtensionType::ServerName,
        ExtensionType::ExtendedMasterSecret,
        ExtensionType::RenegotiationInfo,
        ExtensionType::EllipticCurves,
        ExtensionType::ECPointFormats,
        ExtensionType::SessionTicket,
        ExtensionType::ALProtocolNegotiation,
        ExtensionType::StatusRequest,
        ExtensionType::SignatureAlgorithms,
        ExtensionType::SCT,
        ExtensionType::KeyShare,
        ExtensionType::PSKKeyExchangeModes,
        ExtensionType::EarlyData,
        ExtensionType::SupportedVersions,
        ExtensionType::CompressCertificate,
        ExtensionType::ApplicationSettingsOld,
        ExtensionType::ApplicationSettings,
        ExtensionType::NextProtocolNegotiation,
        ExtensionType::ChannelId,
        ExtensionType::UseSRTP,
        ExtensionType::SignatureAlgorithmsCert,
        ExtensionType::DelegatedCredential,
        ExtensionType::RecordSizeLimit,
        ExtensionType::PostHandshakeAuth,
        ExtensionType::ClientCertificateType,
        ExtensionType::ServerCertificateType,
        ExtensionType::CertificateAuthorities,
        ExtensionType::QuicTransportParametersLegacy,
        ExtensionType::TrustAnchors,
        ExtensionType::Pake,
        ExtensionType::PreSharedKey,
        ExtensionType(0x1234),
    ];

    let missing = expected
        .iter()
        .copied()
        .filter(|typ| !extension_types.contains(&u16::from(*typ)))
        .collect::<Vec<_>>();
    assert!(
        missing.is_empty(),
        "missing ClientHello extensions {missing:?}; saw {extension_types:?}"
    );
    assert!(
        extension_types
            .iter()
            .filter(|typ| is_grease_u16(**typ))
            .count()
            >= 2,
        "expected two GREASE extensions; saw {extension_types:?}"
    );
}

fn assert_matrix_client_hello_extensions(case: &MatrixCase, client_hellos: &[Vec<u8>]) {
    let mut extension_types = Vec::new();
    for client_hello in client_hellos {
        extension_types.extend(client_hello_extension_types(client_hello));
    }

    let mut expected = vec![
        ExtensionType::ServerName,
        ExtensionType::EllipticCurves,
        ExtensionType::SignatureAlgorithms,
        ExtensionType::KeyShare,
        ExtensionType::PSKKeyExchangeModes,
        ExtensionType::SupportedVersions,
    ];

    if case
        .extension_groups
        .has(ExtensionGroups::COMPAT)
    {
        expected.extend([
            ExtensionType::ExtendedMasterSecret,
            ExtensionType::RenegotiationInfo,
            ExtensionType::ECPointFormats,
            ExtensionType::SessionTicket,
            ExtensionType::NextProtocolNegotiation,
            ExtensionType::ChannelId,
        ]);
    }
    if case.client_alpn {
        expected.push(ExtensionType::ALProtocolNegotiation);
    }
    if case
        .extension_groups
        .has(ExtensionGroups::CERT)
    {
        expected.extend([
            ExtensionType::StatusRequest,
            ExtensionType::SCT,
            ExtensionType::CompressCertificate,
            ExtensionType::SignatureAlgorithmsCert,
            ExtensionType::DelegatedCredential,
            ExtensionType::RecordSizeLimit,
            ExtensionType::PostHandshakeAuth,
            ExtensionType::ClientCertificateType,
            ExtensionType::ServerCertificateType,
            ExtensionType::CertificateAuthorities,
            ExtensionType::TrustAnchors,
        ]);
    }
    match case.client_alps {
        AlpsMode::None => {}
        AlpsMode::Old => expected.push(ExtensionType::ApplicationSettingsOld),
        AlpsMode::New => expected.push(ExtensionType::ApplicationSettings),
        AlpsMode::Both => expected.extend([
            ExtensionType::ApplicationSettingsOld,
            ExtensionType::ApplicationSettings,
        ]),
    }
    if case
        .extension_groups
        .has(ExtensionGroups::IGNORED)
    {
        expected.extend([
            ExtensionType::UseSRTP,
            ExtensionType::QuicTransportParametersLegacy,
            ExtensionType::Pake,
            ExtensionType(0x1234),
        ]);
    }
    if case.expect_psk_extension() {
        expected.push(ExtensionType::PreSharedKey);
    }
    if case.expect_early_data_extension() {
        expected.push(ExtensionType::EarlyData);
    }

    let missing = expected
        .iter()
        .copied()
        .filter(|typ| !extension_types.contains(&u16::from(*typ)))
        .collect::<Vec<_>>();
    assert!(
        missing.is_empty(),
        "{}: missing ClientHello extensions {missing:?}; saw {extension_types:?}",
        case.name
    );

    let grease_count = extension_types
        .iter()
        .filter(|typ| is_grease_u16(**typ))
        .count();
    if case
        .extension_groups
        .has(ExtensionGroups::GREASE)
    {
        assert!(
            grease_count >= 2,
            "{}: expected two GREASE extensions; saw {extension_types:?}",
            case.name
        );
    }

    if !case.expect_psk_extension() {
        assert!(
            !extension_types.contains(&u16::from(ExtensionType::PreSharedKey)),
            "{}: unexpectedly sent pre_shared_key; saw {extension_types:?}",
            case.name
        );
    }
    if !case.expect_early_data_extension() {
        assert!(
            !extension_types.contains(&u16::from(ExtensionType::EarlyData)),
            "{}: unexpectedly sent early_data; saw {extension_types:?}",
            case.name
        );
    }
}

fn greaseable_curves(groups: ExtensionGroups) -> Vec<GreaseOr<NamedGroup>> {
    use GreaseOr::{Grease, T};

    let mut curves = Vec::new();
    if groups.has(ExtensionGroups::GREASE) {
        curves.push(Grease);
    }
    curves.extend([T(NamedGroup::X25519), T(NamedGroup::secp256r1)]);
    curves
}

fn greaseable_versions(groups: ExtensionGroups) -> Vec<GreaseOr<ProtocolVersion>> {
    use GreaseOr::{Grease, T};

    let mut versions = Vec::new();
    if groups.has(ExtensionGroups::GREASE) {
        versions.push(Grease);
    }
    versions.extend([T(ProtocolVersion::TLSv1_3), T(ProtocolVersion::TLSv1_2)]);
    versions
}

fn greaseable_sigalgs(groups: ExtensionGroups) -> Vec<GreaseOr<SignatureScheme>> {
    use GreaseOr::{Grease, T};

    let mut sigalgs = vec![
        T(SignatureScheme::ECDSA_NISTP256_SHA256),
        T(SignatureScheme::RSA_PSS_SHA256),
        T(SignatureScheme::RSA_PKCS1_SHA256),
        T(SignatureScheme::ECDSA_NISTP384_SHA384),
    ];
    if groups.has(ExtensionGroups::GREASE) {
        sigalgs.push(Grease);
    }
    sigalgs
}

fn greaseable_sigalgs_cert(groups: ExtensionGroups) -> Vec<GreaseOr<SignatureScheme>> {
    use GreaseOr::{Grease, T};

    let mut sigalgs = vec![
        T(SignatureScheme::ECDSA_NISTP256_SHA256),
        T(SignatureScheme::RSA_PSS_SHA256),
        T(SignatureScheme::RSA_PKCS1_SHA256),
    ];
    if groups.has(ExtensionGroups::GREASE) {
        sigalgs.push(Grease);
    }
    sigalgs
}

fn greaseable_delegated_credentials(groups: ExtensionGroups) -> Vec<GreaseOr<SignatureScheme>> {
    use GreaseOr::{Grease, T};

    let mut schemes = vec![
        T(SignatureScheme::ECDSA_NISTP256_SHA256),
        T(SignatureScheme::ECDSA_NISTP384_SHA384),
    ];
    if groups.has(ExtensionGroups::GREASE) {
        schemes.push(Grease);
    }
    schemes
}

fn first_client_hello_record(tls_bytes: &[u8]) -> Vec<u8> {
    let mut offset = 0;
    assert_eq!(take_u8(tls_bytes, &mut offset), 22);
    take(tls_bytes, &mut offset, 2);
    let len = take_u16(tls_bytes, &mut offset) as usize;
    take(tls_bytes, &mut offset, len).to_vec()
}

fn client_hello_extension_types(client_hello: &[u8]) -> Vec<u16> {
    let mut offset = 0;
    assert_eq!(take_u8(client_hello, &mut offset), 1);
    let body_len = take_u24(client_hello, &mut offset);
    assert_eq!(client_hello.len(), offset + body_len);

    take(client_hello, &mut offset, 2);
    take(client_hello, &mut offset, 32);
    let session_id_len = take_u8(client_hello, &mut offset) as usize;
    take(client_hello, &mut offset, session_id_len);
    let cipher_suites_len = take_u16(client_hello, &mut offset) as usize;
    take(client_hello, &mut offset, cipher_suites_len);
    let compression_methods_len = take_u8(client_hello, &mut offset) as usize;
    take(client_hello, &mut offset, compression_methods_len);

    let extensions_len = take_u16(client_hello, &mut offset) as usize;
    let extensions_end = offset + extensions_len;
    assert_eq!(client_hello.len(), extensions_end);

    let mut types = Vec::new();
    while offset < extensions_end {
        let typ = take_u16(client_hello, &mut offset);
        let len = take_u16(client_hello, &mut offset) as usize;
        take(client_hello, &mut offset, len);
        types.push(typ);
    }
    types
}

fn root_ca() -> RootCertStore {
    let mut roots = RootCertStore::empty();
    roots.add_parsable_certificates([CertificateDer::from(
        fs::read(manifest_path(CA_FILE)).unwrap(),
    )]);
    roots
}

fn manifest_path(path: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join(path)
}

fn leak_vec<T: 'static>(items: Vec<T>) -> &'static [T] {
    Box::leak(items.into_boxed_slice())
}

fn nss_version_range(version: ProtocolVersion) -> &'static str {
    match version {
        ProtocolVersion::TLSv1_2 => "tls1.2:tls1.2",
        ProtocolVersion::TLSv1_3 => "tls1.3:tls1.3",
        _ => panic!("unsupported NSS test version {version:?}"),
    }
}

fn nss_group_name(group: NamedGroup) -> &'static str {
    match group {
        NamedGroup::X25519 => "x25519",
        NamedGroup::secp256r1 => "P256",
        _ => panic!("unsupported NSS test group {group:?}"),
    }
}

fn is_grease_u16(value: u16) -> bool {
    value & 0x0f0f == 0x0a0a && value >> 8 == value & 0xff
}

fn take<'a>(bytes: &'a [u8], offset: &mut usize, len: usize) -> &'a [u8] {
    let end = offset.checked_add(len).unwrap();
    assert!(end <= bytes.len());
    let taken = &bytes[*offset..end];
    *offset = end;
    taken
}

fn take_u8(bytes: &[u8], offset: &mut usize) -> u8 {
    take(bytes, offset, 1)[0]
}

fn take_u16(bytes: &[u8], offset: &mut usize) -> u16 {
    let bytes = take(bytes, offset, 2);
    u16::from_be_bytes([bytes[0], bytes[1]])
}

fn take_u24(bytes: &[u8], offset: &mut usize) -> usize {
    let bytes = take(bytes, offset, 3);
    ((bytes[0] as usize) << 16) | ((bytes[1] as usize) << 8) | bytes[2] as usize
}

struct NssTools {
    selfserv: PathBuf,
    certutil: PathBuf,
    pk12util: PathBuf,
    lib_dir: Option<PathBuf>,
}

impl NssTools {
    fn find() -> Self {
        let selfserv = find_tool(
            "NSS_SELFSERV",
            "selfserv",
            &[
                "../reference/dist/Debug/bin/selfserv",
                "../reference/dist/Release/bin/selfserv",
                "../reference/nss/out/Debug/selfserv",
                "../reference/nss/out/Release/selfserv",
            ],
        );
        let certutil = find_tool(
            "NSS_CERTUTIL",
            "certutil",
            &[
                "../reference/dist/Debug/bin/certutil",
                "../reference/dist/Release/bin/certutil",
                "../reference/nss/out/Debug/certutil",
                "../reference/nss/out/Release/certutil",
            ],
        );
        let pk12util = find_tool(
            "NSS_PK12UTIL",
            "pk12util",
            &[
                "../reference/dist/Debug/bin/pk12util",
                "../reference/dist/Release/bin/pk12util",
                "../reference/nss/out/Debug/pk12util",
                "../reference/nss/out/Release/pk12util",
            ],
        );
        let lib_dir = selfserv
            .parent()
            .and_then(|bin| bin.parent())
            .map(|dist| dist.join("lib"))
            .filter(|path| path.is_dir());

        Self {
            selfserv,
            certutil,
            pk12util,
            lib_dir,
        }
    }

    fn configure_runtime(&self, command: &mut Command) {
        let Some(lib_dir) = &self.lib_dir else {
            return;
        };
        prepend_path_env(command, "LD_LIBRARY_PATH", lib_dir);
        prepend_path_env(command, "DYLD_LIBRARY_PATH", lib_dir);
    }
}

fn find_tool(env_var: &str, program: &str, candidates: &[&str]) -> PathBuf {
    if let Some(path) = std::env::var_os(env_var) {
        let path = PathBuf::from(path);
        assert!(
            path.is_file(),
            "{env_var} points to a missing file: {path:?}"
        );
        return path;
    }

    for candidate in candidates {
        let path = manifest_path(candidate);
        if path.is_file() {
            return path;
        }
    }

    if let Some(path) = find_on_path(program) {
        return path;
    }

    panic!("could not find {program}; set {env_var} or build reference/nss");
}

fn find_on_path(program: &str) -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    std::env::split_paths(&path)
        .map(|dir| dir.join(program))
        .find(|path| path.is_file())
}

fn prepend_path_env(command: &mut Command, key: &str, path: &Path) {
    let value = match std::env::var_os(key) {
        Some(existing) => {
            let mut paths = vec![path.to_path_buf()];
            paths.extend(std::env::split_paths(&existing));
            std::env::join_paths(paths).unwrap()
        }
        None => path.as_os_str().to_os_string(),
    };
    command.env(key, value);
}

struct NssDb {
    path: PathBuf,
}

impl NssDb {
    fn new(tools: &NssTools) -> Self {
        let path = unique_temp_dir("craftls-nss-db");
        fs::create_dir_all(&path).unwrap();

        let db = Self { path };
        run_tool(
            &tools.certutil,
            tools,
            &["-N", "-d", &db.dir_arg(), "--empty-password"],
            "certutil -N",
        );

        let p12 = db.path.join("server.p12");
        run_tool(
            Path::new("openssl"),
            tools,
            &[
                "pkcs12",
                "-export",
                "-inkey",
                manifest_path(PRIV_KEY_FILE)
                    .to_str()
                    .unwrap(),
                "-in",
                manifest_path(CERT_FILE)
                    .to_str()
                    .unwrap(),
                "-certfile",
                manifest_path(CA_PEM_FILE)
                    .to_str()
                    .unwrap(),
                "-name",
                SERVER_NICKNAME,
                "-out",
                p12.to_str().unwrap(),
                "-passout",
                "pass:",
            ],
            "openssl pkcs12",
        );

        run_tool(
            &tools.pk12util,
            tools,
            &[
                "-i",
                p12.to_str().unwrap(),
                "-d",
                &db.dir_arg(),
                "-K",
                "",
                "-W",
                "",
            ],
            "pk12util -i",
        );

        db
    }

    fn dir_arg(&self) -> String {
        format!("sql:{}", self.path.display())
    }
}

impl Drop for NssDb {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}

fn run_tool(program: &Path, tools: &NssTools, args: &[&str], label: &str) {
    let mut command = Command::new(program);
    tools.configure_runtime(&mut command);
    let output = command
        .args(args)
        .output()
        .unwrap_or_else(|err| panic!("failed to run {label}: {err}"));
    assert!(
        output.status.success(),
        "{label} failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn unique_temp_dir(prefix: &str) -> PathBuf {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    std::env::temp_dir().join(format!("{prefix}-{}-{nanos}", std::process::id()))
}

struct RecordingStream {
    inner: TcpStream,
    writes: Vec<u8>,
}

impl RecordingStream {
    fn new(inner: TcpStream) -> Self {
        Self {
            inner,
            writes: Vec::new(),
        }
    }
}

impl Read for RecordingStream {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        self.inner.read(buf)
    }
}

impl Write for RecordingStream {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        let written = self.inner.write(buf)?;
        self.writes
            .extend_from_slice(&buf[..written]);
        Ok(written)
    }

    fn flush(&mut self) -> std::io::Result<()> {
        self.inner.flush()
    }
}

struct NssServerConfig {
    version: ProtocolVersion,
    group: NamedGroup,
    alpn: bool,
    tickets: bool,
    early_data: bool,
    no_cache: bool,
    cert_compression: bool,
}

struct NssServer {
    child: Option<Child>,
    port: u16,
}

impl NssServer {
    fn wait_with_output(&mut self) -> Output {
        self.child
            .take()
            .unwrap()
            .wait_with_output()
            .unwrap()
    }

    fn exited_output(&mut self) -> Option<Output> {
        if self
            .child
            .as_mut()
            .unwrap()
            .try_wait()
            .unwrap()
            .is_some()
        {
            Some(
                self.child
                    .take()
                    .unwrap()
                    .wait_with_output()
                    .unwrap(),
            )
        } else {
            None
        }
    }
}

impl Drop for NssServer {
    fn drop(&mut self) {
        let Some(child) = &mut self.child else {
            return;
        };
        if child
            .try_wait()
            .ok()
            .flatten()
            .is_none()
        {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

struct MatrixCase {
    name: String,
    version: ProtocolVersion,
    group: NamedGroup,
    server_alpn: bool,
    client_alpn: bool,
    client_alps: AlpsMode,
    tickets: bool,
    server_early_data: bool,
    client_early_data: bool,
    extension_groups: ExtensionGroups,
}

impl MatrixCase {
    fn expect_psk_extension(&self) -> bool {
        self.version == ProtocolVersion::TLSv1_3 && self.tickets
    }

    fn expect_early_data_extension(&self) -> bool {
        self.expect_psk_extension() && self.server_early_data && self.client_early_data
    }
}

#[derive(Clone, Copy, Debug)]
enum AlpsMode {
    None,
    Old,
    New,
    Both,
}

#[derive(Clone, Copy)]
struct ExtensionGroups(u8);

impl ExtensionGroups {
    const GREASE: u8 = 1 << 0;
    const COMPAT: u8 = 1 << 1;
    const CERT: u8 = 1 << 2;
    const IGNORED: u8 = 1 << 3;
    const COMPAT_GROUP: Self = Self(Self::COMPAT);

    fn all() -> Self {
        Self(Self::GREASE | Self::COMPAT | Self::CERT | Self::IGNORED)
    }

    fn has(self, group: u8) -> bool {
        self.0 & group != 0
    }
}

const SERVER_NICKNAME: &str = "localhost";
const CERT_FILE: &str = "../test-ca/rsa-2048/end.cert";
const PRIV_KEY_FILE: &str = "../test-ca/rsa-2048/end.key";
const CA_FILE: &str = "../test-ca/rsa-2048/ca.der";
const CA_PEM_FILE: &str = "../test-ca/rsa-2048/ca.cert";
const HTTP1: &[u8] = b"http/1.1";
const HTTP1_PROTOCOLS: &[&[u8]] = &[HTTP1];
const NSS_EOF_MARKER: &[u8] = b"EOF\r\n\r\n\r\n";
const EMPTY_DER_NAME: &[u8] = b"\x30\x00";
const NSS_IGNORED_PAKE_CLIENT_HELLO: &[u8] = &[
    0x00, 0x00, // client_identity
    0x00, 0x00, // server_identity
    0x00, 0x04, // client_shares
    0x7d, 0x96, // SPAKE2PLUS_V1
    0x00, 0x00, // empty pake_message
];
