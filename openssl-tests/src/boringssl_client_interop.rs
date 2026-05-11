use std::fs;
use std::io::{ErrorKind, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::path::PathBuf;
use std::process::{Child, Command, Output, Stdio};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use rustls::craft::{
    self, ApplicationSettingsCodepoint, CraftExtension, ExtensionSpec, Fingerprint, GreaseOr,
    GreaseOrProtocol, GreaseOrPskKeyExchangeMode, KeepExtension,
};
use rustls::enums::{CertificateCompressionAlgorithm, CertificateType};
use rustls::pki_types::{CertificateDer, ServerName};
use rustls::{
    CipherSuite, ClientConfig, Connection, ExtensionType, NamedGroup, ProtocolVersion,
    RootCertStore, SignatureScheme,
};
use rustls_util::{Stream, complete_io};

#[test]
#[ignore = "requires a built BoringSSL bssl_shim; set BORINGSSL_BSSL_SHIM or build reference/boringssl"]
fn craftls_client_all_extensions_connects_to_boringssl_server() {
    let shim = find_boringssl_shim();
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    listener.set_nonblocking(true).unwrap();
    let port = listener.local_addr().unwrap().port();
    let mut shim = spawn_boringssl_server(
        &shim,
        port,
        &BoringSslServerConfig {
            version: ProtocolVersion::TLSv1_3,
            selected_alpn: Some(HTTP1),
            alps: AlpsMode::None,
            expect_peer_application_settings: false,
            tickets: true,
            early_data: true,
            group: NamedGroup::X25519,
        },
    );

    let config = craft_client_config(all_extensions_fingerprint(), true);
    let first = run_craft_client_to_boringssl(
        "all extensions/full",
        &listener,
        config.clone(),
        &mut shim,
        ProtocolVersion::TLSv1_3,
        Some(HTTP1),
    );
    let second = run_craft_client_to_boringssl(
        "all extensions/resume",
        &listener,
        config,
        &mut shim,
        ProtocolVersion::TLSv1_3,
        Some(HTTP1),
    );

    let output = shim.wait_with_output();
    assert!(
        output.status.success(),
        "BoringSSL shim failed\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert_client_hello_covers_all_extensions(&[first, second]);
}

#[test]
#[ignore = "requires a built BoringSSL bssl_shim; set BORINGSSL_BSSL_SHIM or build reference/boringssl"]
fn craftls_client_boringssl_extension_configuration_matrix() {
    let shim = find_boringssl_shim();
    for case in extension_matrix_cases() {
        run_matrix_case(&shim, &case);
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

fn run_craft_client_to_boringssl(
    case_name: &str,
    listener: &TcpListener,
    config: Arc<ClientConfig>,
    shim: &mut BoringSslShim,
    expected_version: ProtocolVersion,
    expected_alpn: Option<&[u8]>,
) -> Vec<u8> {
    let mut io = RecordingStream::new(accept_boringssl_connection(listener, shim));
    let mut shim_id = [0; 8];
    io.inner
        .read_exact(&mut shim_id)
        .unwrap();
    assert_eq!(u64::from_le_bytes(shim_id), 0);

    let server_name = ServerName::try_from("localhost")
        .unwrap()
        .to_owned();
    let mut client = config
        .connect(server_name)
        .build()
        .unwrap();

    complete_io(&mut io, &mut client).unwrap();
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
    let msg = b"boringssl extension coverage";
    {
        let mut tls = Stream::new(&mut client, &mut io);
        tls.write_all(msg)
            .unwrap_or_else(|err| panic!("{case_name}: failed to write app data: {err:?}"));
        let mut echoed = vec![0; msg.len()];
        if let Err(err) = tls.read_exact(&mut echoed) {
            let output = shim.stop_with_output();
            panic!(
                "{case_name}: failed to read app data: {err:?}\nstdout:\n{}\nstderr:\n{}",
                String::from_utf8_lossy(&output.stdout),
                String::from_utf8_lossy(&output.stderr)
            );
        }
        assert_eq!(
            echoed,
            msg.iter()
                .map(|byte| byte ^ 0xff)
                .collect::<Vec<_>>()
        );
    }

    client.send_close_notify();
    complete_io(&mut io, &mut client)
        .unwrap_or_else(|err| panic!("{case_name}: failed to complete close_notify: {err:?}"));
    client_hello
}

fn spawn_boringssl_server(
    shim: &PathBuf,
    port: u16,
    config: &BoringSslServerConfig,
) -> BoringSslShim {
    let mut command = Command::new(shim);
    command
        .arg("-server")
        .arg("-port")
        .arg(port.to_string())
        .arg("-shim-id")
        .arg("0")
        .arg("-resume-count")
        .arg("1");

    if !config.tickets {
        command
            .arg("-no-ticket")
            .arg("-expect-session-miss");
    }
    if config.early_data {
        command.arg("-enable-early-data");
    }

    command
        .arg("-min-version")
        .arg(u16::from(config.version).to_string())
        .arg("-max-version")
        .arg(u16::from(config.version).to_string())
        .arg("-expect-version")
        .arg(u16::from(config.version).to_string())
        .arg("-expect-server-name")
        .arg("localhost");

    if let Some(selected_alpn) = config.selected_alpn {
        command
            .arg("-select-alpn")
            .arg(std::str::from_utf8(selected_alpn).unwrap());
    }

    match config.alps {
        AlpsMode::None => {}
        AlpsMode::Old => {
            command
                .arg("-application-settings")
                .arg(format!(
                    "{},server-settings",
                    std::str::from_utf8(HTTP1).unwrap()
                ))
                .arg("-alps-use-new-codepoint")
                .arg("0");
        }
        AlpsMode::New | AlpsMode::Both => {
            command
                .arg("-application-settings")
                .arg(format!(
                    "{},server-settings",
                    std::str::from_utf8(HTTP1).unwrap()
                ));
        }
    }
    if config.expect_peer_application_settings {
        command
            .arg("-expect-peer-application-settings")
            .arg("");
    }

    let child = command
        .arg("-curves")
        .arg(u16::from(config.group).to_string())
        .arg("-expect-curve-id")
        .arg(u16::from(config.group).to_string())
        .arg("-cert-file")
        .arg(manifest_path(CERT_CHAIN_FILE))
        .arg("-key-file")
        .arg(manifest_path(PRIV_KEY_FILE))
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap_or_else(|err| panic!("failed to start BoringSSL shim {shim:?}: {err}"));
    BoringSslShim { child: Some(child) }
}

fn accept_boringssl_connection(listener: &TcpListener, shim: &mut BoringSslShim) -> TcpStream {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match listener.accept() {
            Ok((stream, _addr)) => {
                stream
                    .set_read_timeout(Some(Duration::from_secs(10)))
                    .unwrap();
                stream
                    .set_write_timeout(Some(Duration::from_secs(10)))
                    .unwrap();
                return stream;
            }
            Err(err) if err.kind() == ErrorKind::WouldBlock => {
                if let Some(output) = shim.exited_output() {
                    panic!(
                        "BoringSSL shim exited before connecting: {}\nstdout:\n{}\nstderr:\n{}",
                        output.status,
                        String::from_utf8_lossy(&output.stdout),
                        String::from_utf8_lossy(&output.stderr)
                    );
                }
                assert!(
                    Instant::now() <= deadline,
                    "timed out waiting for BoringSSL shim"
                );
                thread::sleep(Duration::from_millis(10));
            }
            Err(err) => panic!("failed to accept BoringSSL shim connection: {err}"),
        }
    }
}

fn extension_matrix_cases() -> Vec<MatrixCase> {
    let mut cases = Vec::new();
    let all_groups = ExtensionGroups::all();

    for bits in 0..16 {
        cases.push(MatrixCase {
            name: format!("client-extension-groups-{bits:04b}"),
            version: ProtocolVersion::TLSv1_3,
            group: NamedGroup::X25519,
            server_alpn: false,
            client_alpn: false,
            server_alps: AlpsMode::None,
            client_alps: AlpsMode::None,
            tickets: true,
            server_early_data: false,
            client_early_data: false,
            extension_groups: ExtensionGroups(bits),
        });
    }

    for client_alps in [AlpsMode::None, AlpsMode::Old, AlpsMode::New, AlpsMode::Both] {
        for server_alps in [AlpsMode::None, AlpsMode::Old, AlpsMode::New] {
            // Negotiated server ALPS requires client application-settings messages;
            // craft currently covers the ClientHello advertisement surface here.
            if server_alps.negotiates_with(client_alps) {
                continue;
            }
            cases.push(MatrixCase {
                name: format!("alpn-alps-client-{client_alps:?}-server-{server_alps:?}"),
                version: ProtocolVersion::TLSv1_3,
                group: NamedGroup::X25519,
                server_alpn: true,
                client_alpn: true,
                server_alps,
                client_alps,
                tickets: true,
                server_early_data: false,
                client_early_data: false,
                extension_groups: all_groups,
            });
        }
    }

    for tickets in [false, true] {
        for server_early_data in [false, true] {
            if server_early_data && !tickets {
                continue;
            }
            for client_early_data in [false, true] {
                cases.push(MatrixCase {
                    name: format!(
                        "tickets-{tickets}-server-early-{server_early_data}-client-early-{client_early_data}"
                    ),
                    version: ProtocolVersion::TLSv1_3,
                    group: NamedGroup::X25519,
                    server_alpn: true,
                    client_alpn: true,
                    server_alps: AlpsMode::None,
                    client_alps: AlpsMode::Both,
                    tickets,
                    server_early_data,
                    client_early_data,
                    extension_groups: all_groups,
                });
            }
        }
    }

    for version in [ProtocolVersion::TLSv1_2, ProtocolVersion::TLSv1_3] {
        for group in [NamedGroup::X25519, NamedGroup::secp256r1] {
            for client_alpn in [false, true] {
                for server_alpn in [false, true] {
                    if server_alpn && !client_alpn {
                        continue;
                    }
                    cases.push(MatrixCase {
                        name: format!(
                            "version-{version:?}-group-{group:?}-client-alpn-{client_alpn}-server-alpn-{server_alpn}"
                        ),
                        version,
                        group,
                        server_alpn,
                        client_alpn,
                        server_alps: AlpsMode::None,
                        client_alps: if version == ProtocolVersion::TLSv1_3 && client_alpn {
                            AlpsMode::Both
                        } else {
                            AlpsMode::None
                        },
                        tickets: true,
                        server_early_data: false,
                        client_early_data: false,
                        extension_groups: all_groups,
                    });
                }
            }
        }
    }

    cases
}

fn run_matrix_case(shim: &PathBuf, case: &MatrixCase) {
    let listener = TcpListener::bind(("127.0.0.1", 0)).unwrap();
    listener.set_nonblocking(true).unwrap();
    let port = listener.local_addr().unwrap().port();
    let mut shim = spawn_boringssl_server(
        shim,
        port,
        &BoringSslServerConfig {
            version: case.version,
            selected_alpn: case.server_alpn.then_some(HTTP1),
            alps: case.server_alps,
            expect_peer_application_settings: case
                .server_alps
                .negotiates_with(case.client_alps),
            tickets: case.tickets,
            early_data: case.server_early_data,
            group: case.group,
        },
    );

    let config = craft_client_config(matrix_fingerprint(case), case.client_early_data);
    let expected_alpn = (case.server_alpn && case.client_alpn).then_some(HTTP1);
    let first = run_craft_client_to_boringssl(
        &format!("{}/full", case.name),
        &listener,
        config.clone(),
        &mut shim,
        case.version,
        expected_alpn,
    );
    let second = run_craft_client_to_boringssl(
        &format!("{}/resume", case.name),
        &listener,
        config,
        &mut shim,
        case.version,
        expected_alpn,
    );

    let output = shim.wait_with_output();
    assert!(
        output.status.success(),
        "{}: BoringSSL shim failed\nstdout:\n{}\nstderr:\n{}",
        case.name,
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );

    assert_matrix_client_hello_extensions(case, &[first, second]);
}

fn matrix_fingerprint(case: &MatrixCase) -> Fingerprint {
    use ExtensionSpec::{Craft, Keep, Rustls};
    use GreaseOr::{Grease, T};
    use KeepExtension::{Must, Optional};

    let mut extensions = Vec::new();
    if case
        .extension_groups
        .has(ExtensionGroups::GREASE)
    {
        extensions.push(Craft(CraftExtension::Grease1));
    }

    extensions.push(Keep(Must(ExtensionType::ServerName)));

    if case
        .extension_groups
        .has(ExtensionGroups::COMPAT)
    {
        extensions.push(Rustls(craft::ClientExtension::ExtendedMasterSecretRequest));
        extensions.push(Craft(CraftExtension::RenegotiationInfo));
    }

    extensions.push(Craft(CraftExtension::SupportedCurves(leak_vec(
        greaseable_curves(case.extension_groups),
    ))));

    if case
        .extension_groups
        .has(ExtensionGroups::COMPAT)
    {
        extensions.push(Rustls(craft::ClientExtension::EcPointFormats(vec![
            craft::ECPointFormat::Uncompressed,
        ])));
        extensions.push(Keep(Optional(ExtensionType::SessionTicket)));
    }

    if case.client_alpn {
        if case
            .extension_groups
            .has(ExtensionGroups::GREASE)
        {
            extensions.push(Craft(CraftExtension::ProtocolsWithGrease(leak_vec(vec![
                GreaseOrProtocol::Protocol(HTTP1),
                GreaseOrProtocol::Grease,
            ]))));
        } else {
            extensions.push(Craft(CraftExtension::Protocols(HTTP1_PROTOCOLS)));
        }
    }

    if case
        .extension_groups
        .has(ExtensionGroups::CERT)
    {
        extensions.push(Rustls(craft::ClientExtension::CertificateStatusRequest(
            craft::ocsp_req(),
        )));
    }

    extensions.push(Craft(CraftExtension::SignatureAlgorithms(leak_vec(
        greaseable_sigalgs(case.extension_groups),
    ))));

    if case
        .extension_groups
        .has(ExtensionGroups::CERT)
    {
        extensions.push(Craft(CraftExtension::SignedCertificateTimestamp));
    }

    let mut key_shares = Vec::new();
    if case
        .extension_groups
        .has(ExtensionGroups::GREASE)
    {
        key_shares.push(Grease);
    }
    key_shares.push(T(NamedGroup::X25519));
    extensions.push(Craft(CraftExtension::KeyShare(leak_vec(key_shares))));

    let mut psk_modes = vec![GreaseOrPskKeyExchangeMode::T(
        craft::PSKKeyExchangeMode::PSK_DHE_KE,
    )];
    if case
        .extension_groups
        .has(ExtensionGroups::GREASE)
    {
        psk_modes.push(GreaseOrPskKeyExchangeMode::Grease);
    }
    extensions.push(Craft(CraftExtension::PresharedKeyModes(leak_vec(
        psk_modes,
    ))));

    if case.client_early_data {
        extensions.push(Keep(Optional(ExtensionType::EarlyData)));
    }

    extensions.push(Craft(CraftExtension::SupportedVersions(leak_vec(
        greaseable_versions(case.extension_groups),
    ))));

    if case
        .extension_groups
        .has(ExtensionGroups::CERT)
    {
        extensions.push(Craft(CraftExtension::CompressCert(&[
            CertificateCompressionAlgorithm::Brotli,
        ])));
    }

    match case.client_alps {
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

    if case
        .extension_groups
        .has(ExtensionGroups::GREASE)
    {
        extensions.push(Craft(CraftExtension::Grease2));
    }

    if case
        .extension_groups
        .has(ExtensionGroups::COMPAT)
    {
        extensions.push(Craft(CraftExtension::NextProtocolNegotiation));
        extensions.push(Craft(CraftExtension::ChannelId));
    }

    if case
        .extension_groups
        .has(ExtensionGroups::IGNORED)
    {
        extensions.push(Craft(CraftExtension::UseSrtp {
            profiles: &[0x0001],
            mki: &[],
        }));
    }

    if case
        .extension_groups
        .has(ExtensionGroups::CERT)
    {
        extensions.push(Craft(CraftExtension::SignatureAlgorithmsCert(leak_vec(
            greaseable_sigalgs_cert(case.extension_groups),
        ))));
        extensions.push(Craft(CraftExtension::DelegatedCredentials(leak_vec(
            greaseable_delegated_credentials(case.extension_groups),
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

    if case
        .extension_groups
        .has(ExtensionGroups::IGNORED)
    {
        // BoringSSL rejects the IANA QUIC transport parameters codepoint in TCP TLS.
        extensions.push(Craft(CraftExtension::QuicTransportParametersLegacy(&[
            1, 2, 3,
        ])));
        extensions.push(Craft(CraftExtension::Pake(
            BORINGSSL_IGNORED_PAKE_CLIENT_HELLO,
        )));
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

fn leak_vec<T: 'static>(items: Vec<T>) -> &'static [T] {
    Box::leak(items.into_boxed_slice())
}

fn all_extensions_fingerprint() -> Fingerprint {
    use ExtensionSpec::{Craft, Keep, Rustls};
    use GreaseOr::{Grease, T};
    use KeepExtension::{Must, Optional};

    let extensions = Box::leak(
        vec![
            Craft(CraftExtension::Grease1),
            Keep(Must(ExtensionType::ServerName)),
            Rustls(craft::ClientExtension::ExtendedMasterSecretRequest),
            Craft(CraftExtension::RenegotiationInfo),
            Craft(CraftExtension::SupportedCurves(Box::leak(
                vec![Grease, T(NamedGroup::X25519), T(NamedGroup::secp256r1)].into_boxed_slice(),
            ))),
            Rustls(craft::ClientExtension::EcPointFormats(vec![
                craft::ECPointFormat::Uncompressed,
            ])),
            Keep(Optional(ExtensionType::SessionTicket)),
            Craft(CraftExtension::ProtocolsWithGrease(Box::leak(
                vec![
                    GreaseOrProtocol::Protocol(b"http/1.1"),
                    GreaseOrProtocol::Grease,
                ]
                .into_boxed_slice(),
            ))),
            Rustls(craft::ClientExtension::CertificateStatusRequest(
                craft::ocsp_req(),
            )),
            Craft(CraftExtension::SignatureAlgorithms(Box::leak(
                vec![
                    T(SignatureScheme::ECDSA_NISTP256_SHA256),
                    T(SignatureScheme::RSA_PSS_SHA256),
                    T(SignatureScheme::RSA_PKCS1_SHA256),
                    T(SignatureScheme::ECDSA_NISTP384_SHA384),
                    Grease,
                ]
                .into_boxed_slice(),
            ))),
            Craft(CraftExtension::SignedCertificateTimestamp),
            Craft(CraftExtension::KeyShare(Box::leak(
                vec![Grease, T(NamedGroup::X25519)].into_boxed_slice(),
            ))),
            Craft(CraftExtension::PresharedKeyModes(Box::leak(
                vec![
                    GreaseOrPskKeyExchangeMode::T(craft::PSKKeyExchangeMode::PSK_DHE_KE),
                    GreaseOrPskKeyExchangeMode::Grease,
                ]
                .into_boxed_slice(),
            ))),
            Keep(Optional(ExtensionType::EarlyData)),
            Craft(CraftExtension::SupportedVersions(Box::leak(
                vec![
                    Grease,
                    T(ProtocolVersion::TLSv1_3),
                    T(ProtocolVersion::TLSv1_2),
                ]
                .into_boxed_slice(),
            ))),
            Craft(CraftExtension::CompressCert(&[
                CertificateCompressionAlgorithm::Brotli,
            ])),
            Craft(CraftExtension::ApplicationSettings {
                codepoint: ApplicationSettingsCodepoint::Old,
                protocols: &[b"http/1.1"],
            }),
            Craft(CraftExtension::ApplicationSettings {
                codepoint: ApplicationSettingsCodepoint::New,
                protocols: &[b"http/1.1"],
            }),
            Craft(CraftExtension::Grease2),
            Craft(CraftExtension::Padding),
            Craft(CraftExtension::NextProtocolNegotiation),
            Craft(CraftExtension::ChannelId),
            Craft(CraftExtension::UseSrtp {
                profiles: &[0x0001],
                mki: &[],
            }),
            Craft(CraftExtension::SignatureAlgorithmsCert(Box::leak(
                vec![
                    T(SignatureScheme::ECDSA_NISTP256_SHA256),
                    T(SignatureScheme::RSA_PSS_SHA256),
                    T(SignatureScheme::RSA_PKCS1_SHA256),
                    Grease,
                ]
                .into_boxed_slice(),
            ))),
            Craft(CraftExtension::DelegatedCredentials(Box::leak(
                vec![
                    T(SignatureScheme::ECDSA_NISTP256_SHA256),
                    T(SignatureScheme::ECDSA_NISTP384_SHA384),
                    Grease,
                ]
                .into_boxed_slice(),
            ))),
            Craft(CraftExtension::RecordSizeLimit(0x4001)),
            Craft(CraftExtension::PostHandshakeAuth),
            Craft(CraftExtension::ClientCertificateTypes(&[
                CertificateType::RawPublicKey,
                CertificateType::X509,
            ])),
            Craft(CraftExtension::ServerCertificateTypes(&[
                CertificateType::RawPublicKey,
                CertificateType::X509,
            ])),
            Craft(CraftExtension::CertificateAuthorities(&[EMPTY_DER_NAME])),
            // BoringSSL rejects the IANA QUIC transport parameters codepoint in TCP TLS.
            Craft(CraftExtension::QuicTransportParametersLegacy(&[1, 2, 3])),
            Craft(CraftExtension::TrustAnchors(&[b"ta"])),
            Craft(CraftExtension::Pake(BORINGSSL_IGNORED_PAKE_CLIENT_HELLO)),
            Craft(CraftExtension::Raw(ExtensionType(0x1234), &[9])),
            Keep(Optional(ExtensionType::PreSharedKey)),
        ]
        .into_boxed_slice(),
    );

    Fingerprint {
        extensions,
        cipher: Box::leak(
            vec![
                craft::GreaseOrCipher::Grease,
                CipherSuite::TLS13_AES_128_GCM_SHA256.into(),
                CipherSuite::TLS13_AES_256_GCM_SHA384.into(),
                CipherSuite::TLS13_CHACHA20_POLY1305_SHA256.into(),
                CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256.into(),
                CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256.into(),
            ]
            .into_boxed_slice(),
        ),
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
        ExtensionType::Padding,
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

fn find_boringssl_shim() -> PathBuf {
    if let Some(path) = std::env::var_os("BORINGSSL_BSSL_SHIM") {
        let path = PathBuf::from(path);
        assert!(
            path.is_file(),
            "BORINGSSL_BSSL_SHIM points to a missing file: {path:?}"
        );
        return path;
    }

    let candidates = [
        manifest_path("../reference/boringssl/build/ssl/test/bssl_shim"),
        manifest_path("../reference/boringssl/build64/ssl/test/bssl_shim"),
        manifest_path("../reference/boringssl/out/Default/ssl/test/bssl_shim"),
        manifest_path("../reference/boringssl/out/linux-x86_64/ssl/test/bssl_shim"),
    ];
    candidates
        .iter()
        .find(|path| path.is_file())
        .cloned()
        .unwrap_or_else(|| {
            panic!(
                "could not find BoringSSL bssl_shim; set BORINGSSL_BSSL_SHIM or build reference/boringssl"
            )
        })
}

fn manifest_path(path: &str) -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join(path)
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

struct BoringSslServerConfig {
    version: ProtocolVersion,
    selected_alpn: Option<&'static [u8]>,
    alps: AlpsMode,
    expect_peer_application_settings: bool,
    tickets: bool,
    early_data: bool,
    group: NamedGroup,
}

struct MatrixCase {
    name: String,
    version: ProtocolVersion,
    group: NamedGroup,
    server_alpn: bool,
    client_alpn: bool,
    server_alps: AlpsMode,
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

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum AlpsMode {
    None,
    Old,
    New,
    Both,
}

impl AlpsMode {
    fn negotiates_with(self, client: Self) -> bool {
        matches!(
            (self, client),
            (Self::Old, Self::Old | Self::Both) | (Self::New, Self::New | Self::Both)
        )
    }
}

#[derive(Clone, Copy)]
struct ExtensionGroups(u8);

impl ExtensionGroups {
    const GREASE: u8 = 1 << 0;
    const COMPAT: u8 = 1 << 1;
    const CERT: u8 = 1 << 2;
    const IGNORED: u8 = 1 << 3;

    fn all() -> Self {
        Self(Self::GREASE | Self::COMPAT | Self::CERT | Self::IGNORED)
    }

    fn has(self, group: u8) -> bool {
        self.0 & group != 0
    }
}

struct BoringSslShim {
    child: Option<Child>,
}

impl BoringSslShim {
    fn wait_with_output(mut self) -> Output {
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

    fn stop_with_output(&mut self) -> Output {
        let mut child = self.child.take().unwrap();
        if child.try_wait().unwrap().is_none() {
            let _ = child.kill();
        }
        child.wait_with_output().unwrap()
    }
}

impl Drop for BoringSslShim {
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

const CERT_CHAIN_FILE: &str = "../test-ca/rsa-2048/end.fullchain";
const PRIV_KEY_FILE: &str = "../test-ca/rsa-2048/end.key";
const CA_FILE: &str = "../test-ca/rsa-2048/ca.der";
const HTTP1: &[u8] = b"http/1.1";
const HTTP1_PROTOCOLS: &[&[u8]] = &[HTTP1];
const EMPTY_DER_NAME: &[u8] = b"\x30\x00";
const BORINGSSL_IGNORED_PAKE_CLIENT_HELLO: &[u8] = &[
    0x00, 0x00, // client_identity
    0x00, 0x00, // server_identity
    0x00, 0x04, // client_shares
    0x7d, 0x96, // SSL_PAKE_SPAKE2PLUSV1
    0x00, 0x00, // empty pake_message, ignored without a PAKE credential
];
