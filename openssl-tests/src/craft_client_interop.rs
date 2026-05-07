use std::fs;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;
use std::thread;
use std::time::Duration;

use openssl::ssl::{AlpnError, SslAcceptor, SslFiletype, SslMethod, SslVersion, select_next_proto};
use rustls::craft::{
    self, ApplicationSettingsCodepoint, CraftExtension, ExtensionSpec, Fingerprint, GreaseOr,
    GreaseOrProtocol, GreaseOrPskKeyExchangeMode, KeepExtension,
};
use rustls::enums::CertificateType;
use rustls::pki_types::{CertificateDer, ServerName};
use rustls::{
    CipherSuite, ClientConfig, ExtensionType, NamedGroup, ProtocolVersion, RootCertStore,
    SignatureScheme,
};
use rustls_aws_lc_rs as provider;
use rustls_util::Stream;

use crate::utils::verify_openssl3_available;

#[test]
fn craft_chrome_108_client_connects_to_openssl_tls13_server() {
    assert_craft_client_connects_to_openssl(
        craft::CHROME_108
            .test_alpn_http1
            .builder(),
        OpenSslServerVersion::Tls13,
        Some(b"http/1.1"),
    );
}

#[test]
fn craft_firefox_105_client_connects_to_openssl_tls12_server() {
    assert_craft_client_connects_to_openssl(
        craft::FIREFOX_105
            .test_alpn_http1
            .builder(),
        OpenSslServerVersion::Tls12,
        Some(b"http/1.1"),
    );
}

#[test]
fn craft_boringssl_nss_extension_surface_connects_to_openssl_tls13_server() {
    assert_craft_client_connects_to_openssl(
        openssl_interop_surface_fingerprint().builder(),
        OpenSslServerVersion::Tls13,
        Some(b"http/1.1"),
    );
}

fn assert_craft_client_connects_to_openssl(
    fingerprint: craft::FingerprintBuilder,
    version: OpenSslServerVersion,
    expected_alpn: Option<&[u8]>,
) {
    verify_openssl3_available();

    let (port, server_thread) = spawn_openssl_server(version, expected_alpn.is_some());
    let config = craft_client_config(fingerprint);
    let server_name = ServerName::try_from("localhost")
        .unwrap()
        .to_owned();
    let mut conn = config
        .connect(server_name)
        .build()
        .unwrap();
    let mut tcp = TcpStream::connect(("localhost", port)).unwrap();
    tcp.set_read_timeout(Some(Duration::from_secs(10)))
        .unwrap();
    tcp.set_write_timeout(Some(Duration::from_secs(10)))
        .unwrap();

    let mut response = String::new();
    {
        let mut tls = Stream::new(&mut conn, &mut tcp);
        tls.read_to_string(&mut response)
            .unwrap();
    }

    assert_eq!(response, SERVER_RESPONSE);
    assert_eq!(conn.protocol_version(), Some(version.protocol_version()));
    assert_eq!(
        conn.alpn_protocol()
            .map(|protocol| protocol.as_ref()),
        expected_alpn
    );

    server_thread.join().unwrap();
}

fn craft_client_config(fingerprint: craft::FingerprintBuilder) -> Arc<ClientConfig> {
    Arc::new(
        ClientConfig::builder(provider::DEFAULT_PROVIDER.into())
            .with_root_certificates(root_ca())
            .with_no_client_auth()
            .unwrap()
            .with_fingerprint(fingerprint),
    )
}

fn spawn_openssl_server(
    version: OpenSslServerVersion,
    enable_alpn: bool,
) -> (u16, thread::JoinHandle<()>) {
    let listener = TcpListener::bind(("localhost", 0)).unwrap();
    let port = listener.local_addr().unwrap().port();
    let acceptor = Arc::new(openssl_acceptor(version, enable_alpn));

    let server_thread = thread::spawn(move || {
        let (tcp, _addr) = listener.accept().unwrap();
        let mut tls = acceptor.accept(tcp).unwrap();
        tls.write_all(SERVER_RESPONSE.as_bytes())
            .unwrap();
        tls.flush().unwrap();
        tls.shutdown().unwrap();
    });

    (port, server_thread)
}

fn openssl_acceptor(version: OpenSslServerVersion, enable_alpn: bool) -> SslAcceptor {
    let mut acceptor = SslAcceptor::mozilla_modern_v5(SslMethod::tls()).unwrap();
    acceptor
        .set_min_proto_version(Some(version.ssl_version()))
        .unwrap();
    acceptor
        .set_max_proto_version(Some(version.ssl_version()))
        .unwrap();
    acceptor
        .set_private_key_file(PRIV_KEY_FILE, SslFiletype::PEM)
        .unwrap();
    acceptor
        .set_certificate_chain_file(CERT_CHAIN_FILE)
        .unwrap();
    acceptor.check_private_key().unwrap();

    if enable_alpn {
        acceptor.set_alpn_select_callback(|_, client| {
            select_next_proto(ALPN_HTTP1, client).ok_or(AlpnError::NOACK)
        });
    }

    acceptor.build()
}

fn openssl_interop_surface_fingerprint() -> Fingerprint {
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
            Craft(CraftExtension::SignatureAlgorithms(Box::leak(
                vec![
                    T(SignatureScheme::ECDSA_NISTP256_SHA256),
                    T(SignatureScheme::RSA_PSS_SHA256),
                    T(SignatureScheme::RSA_PKCS1_SHA256),
                    Grease,
                ]
                .into_boxed_slice(),
            ))),
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
            Craft(CraftExtension::SupportedVersions(Box::leak(
                vec![
                    Grease,
                    T(ProtocolVersion::TLSv1_3),
                    T(ProtocolVersion::TLSv1_2),
                ]
                .into_boxed_slice(),
            ))),
            Craft(CraftExtension::RecordSizeLimit(0x4001)),
            Craft(CraftExtension::ApplicationSettings {
                codepoint: ApplicationSettingsCodepoint::New,
                protocols: &[b"h2"],
            }),
            Craft(CraftExtension::ApplicationSettings {
                codepoint: ApplicationSettingsCodepoint::Old,
                protocols: &[b"h2"],
            }),
            Craft(CraftExtension::NextProtocolNegotiation),
            Craft(CraftExtension::ChannelId),
            Craft(CraftExtension::PostHandshakeAuth),
            Craft(CraftExtension::ClientCertificateTypes(&[
                CertificateType::X509,
            ])),
            Craft(CraftExtension::ServerCertificateTypes(&[
                CertificateType::X509,
            ])),
            Craft(CraftExtension::TrustAnchors(&[b"ta"])),
            Craft(CraftExtension::Pake(&[4, 5])),
            Craft(CraftExtension::Raw(ExtensionType(0x1234), &[9])),
            Craft(CraftExtension::Grease2),
            Craft(CraftExtension::Padding),
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

fn root_ca() -> RootCertStore {
    let mut roots = RootCertStore::empty();
    roots.add_parsable_certificates([CertificateDer::from(fs::read(CA_FILE).unwrap())]);
    roots
}

#[derive(Clone, Copy)]
enum OpenSslServerVersion {
    Tls12,
    Tls13,
}

impl OpenSslServerVersion {
    fn ssl_version(self) -> SslVersion {
        match self {
            Self::Tls12 => SslVersion::TLS1_2,
            Self::Tls13 => SslVersion::TLS1_3,
        }
    }

    fn protocol_version(self) -> ProtocolVersion {
        match self {
            Self::Tls12 => ProtocolVersion::TLSv1_2,
            Self::Tls13 => ProtocolVersion::TLSv1_3,
        }
    }
}

const SERVER_RESPONSE: &str = "Hello from openssl server\n";
const ALPN_HTTP1: &[u8] = b"\x08http/1.1";
const CERT_CHAIN_FILE: &str = "../test-ca/rsa-2048/end.fullchain";
const PRIV_KEY_FILE: &str = "../test-ca/rsa-2048/end.key";
const CA_FILE: &str = "../test-ca/rsa-2048/ca.der";
