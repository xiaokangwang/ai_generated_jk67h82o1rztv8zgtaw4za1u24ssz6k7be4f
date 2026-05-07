#![allow(missing_docs)]

use super::*;
use std::sync::LazyLock;

macro_rules! define_fingerprint {
    ($fingerprint_name:ident { shuffle($extensions:expr), $cipher:expr }) => {
        define_fingerprint!($fingerprint_name, $extensions, true, $cipher);
    };
    ($fingerprint_name:ident { $extensions:expr, $cipher:expr }) => {
        define_fingerprint!($fingerprint_name, $extensions, false, $cipher);
    };
    ($fingerprint_name:ident, $extensions:expr, $shuffle_extensions:expr, $cipher:expr) => {
        /// Represents a set of [`Fingerprint`] configurations, each tailored for different ALPN extensions.
        pub static $fingerprint_name: LazyLock<FingerprintSet> = LazyLock::new(|| {
            use ExtensionSpec::*;

            let no_alpn = ($extensions)
                .iter()
                .filter(|v| !matches!(v, Craft(CraftExtension::Protocols(..))))
                .cloned()
                .collect::<Vec<_>>()
                .into_boxed_slice();

            let alpn_http1 = ($extensions)
                .iter()
                .map(|v| match v {
                    Craft(CraftExtension::Protocols(_)) => {
                        Craft(CraftExtension::Protocols(&[b"http/1.1"]))
                    }
                    v => v.clone(),
                })
                .collect::<Vec<_>>()
                .into_boxed_slice();

            FingerprintSet {
                main: Fingerprint {
                    extensions: ($extensions).as_slice(),
                    cipher: ($cipher).as_slice(),
                    shuffle_extensions: $shuffle_extensions,
                },
                test_alpn_http1: Fingerprint {
                    extensions: Box::leak(alpn_http1),
                    cipher: ($cipher).as_slice(),
                    shuffle_extensions: $shuffle_extensions,
                },
                test_no_alpn: Fingerprint {
                    extensions: Box::leak(no_alpn),
                    cipher: ($cipher).as_slice(),
                    shuffle_extensions: $shuffle_extensions,
                },
            }
        });
    };
}

macro_rules! static_ref {
    ($val:expr, $type:ty) => {{
        static X: $type = $val;
        X
    }};
}

/// The default ocsp request of browsers
pub fn ocsp_req() -> CertificateStatusRequest {
    CertificateStatusRequest::Ocsp(OcspCertificateStatusRequest {
        responder_ids: vec![],
        extensions: PayloadU16(vec![]),
    })
}

/// The signature algorithms of chrome 108
pub static CHROME_108_SIGNATURE_ALGO: &[SignatureScheme] = &[
    SignatureScheme::ECDSA_NISTP256_SHA256,
    SignatureScheme::RSA_PSS_SHA256,
    SignatureScheme::RSA_PKCS1_SHA256,
    SignatureScheme::ECDSA_NISTP384_SHA384,
    SignatureScheme::RSA_PSS_SHA384,
    SignatureScheme::RSA_PKCS1_SHA384,
    SignatureScheme::RSA_PSS_SHA512,
    SignatureScheme::RSA_PKCS1_SHA512,
];

fn default_rustls_session_ticket() -> ClientExtension {
    ClientExtension::SessionTicket(ClientSessionTicket::Offer(Payload::new(Vec::new())))
}

/// The extension list of chrome 108
pub static CHROME_108_EXT: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Craft(CraftExtension::Grease1),
        Keep(Must(ExtensionType::ServerName)),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::SupportedCurves(static_ref!(
            &[
                Grease,
                GreaseOrCurve::T(NamedGroup::X25519),
                GreaseOrCurve::T(NamedGroup::secp256r1),
                GreaseOrCurve::T(NamedGroup::secp384r1),
            ],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Keep(OrDefault(
            ExtensionType::SessionTicket,
            default_rustls_session_ticket(),
        )),
        Craft(CraftExtension::Protocols(&[b"h2", b"http/1.1"])),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Rustls(ClientExtension::SignatureAlgorithms(
            CHROME_108_SIGNATURE_ALGO.to_vec(),
        )),
        Craft(CraftExtension::SignedCertificateTimestamp),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[Grease, GreaseOrCurve::T(NamedGroup::X25519),],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::PresharedKeyModes(vec![
            PSKKeyExchangeMode::PSK_DHE_KE,
        ])),
        Keep(Optional(ExtensionType::EarlyData)),
        Craft(CraftExtension::SupportedVersions(static_ref!(
            &[
                Grease,
                GreaseOrVersion::T(ProtocolVersion::TLSv1_3),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_2),
            ],
            &[GreaseOrVersion]
        ))),
        Keep(Optional(ExtensionType::Cookie)),
        Craft(CraftExtension::CompressCert(static_ref!(
            &[CertificateCompressionAlgorithm::Brotli],
            &[CertificateCompressionAlgorithm]
        ))),
        Craft(CraftExtension::FakeApplicationSettings),
        Craft(CraftExtension::Grease2),
        Craft(CraftExtension::Padding),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

/// The extension list of chrome 108
pub(crate) static EXT_TEST: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Craft(CraftExtension::Grease1),
        Keep(Must(ExtensionType::ServerName)),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::SupportedCurves(static_ref!(
            &[
                Grease,
                GreaseOrCurve::T(NamedGroup::X25519),
                GreaseOrCurve::T(NamedGroup::secp256r1),
                GreaseOrCurve::T(NamedGroup::secp384r1),
            ],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Keep(OrDefault(
            ExtensionType::SessionTicket,
            default_rustls_session_ticket(),
        )),
        Craft(CraftExtension::Protocols(&[b"h2", b"http/1.1"])),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Rustls(ClientExtension::SignatureAlgorithms(
            [
                SignatureScheme::RSA_PSS_SHA512,
                SignatureScheme::RSA_PSS_SHA384,
                SignatureScheme::RSA_PSS_SHA256,
                SignatureScheme::RSA_PKCS1_SHA512,
                SignatureScheme::RSA_PKCS1_SHA384,
                SignatureScheme::RSA_PKCS1_SHA256,
                SignatureScheme::ECDSA_NISTP384_SHA384,
                SignatureScheme::ECDSA_NISTP256_SHA256,
                SignatureScheme::ED25519,
            ]
            .to_vec(),
        )),
        Craft(CraftExtension::SignedCertificateTimestamp),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[Grease, GreaseOrCurve::T(NamedGroup::X25519)],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::PresharedKeyModes(vec![
            PSKKeyExchangeMode::PSK_DHE_KE,
        ])),
        Keep(Optional(ExtensionType::EarlyData)),
        Craft(CraftExtension::SupportedVersions(static_ref!(
            &[
                Grease,
                GreaseOrVersion::T(ProtocolVersion::TLSv1_3),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_2),
            ],
            &[GreaseOrVersion]
        ))),
        Keep(Optional(ExtensionType::Cookie)),
        Craft(CraftExtension::CompressCert(static_ref!(
            &[CertificateCompressionAlgorithm::Brotli],
            &[CertificateCompressionAlgorithm]
        ))),
        // Craft(CraftExtension::FakeApplicationSettings),
        Craft(CraftExtension::Grease2),
        // Craft(CraftExtension::Padding),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

/// The cipher list of chrome 108
///
/// This list includes \*CBC* and \*CipherSuite::TLS_RSA* ciphers for correctness, even though they are not supported by Rustls due to security concerns and deprecation. As these older cipher suites are seldom used in modern secure communications, their absence in Rustls is unlikely to cause compatibility issues.
pub static CHROME_CIPHER: LazyLock<Vec<GreaseOrCipher>> = LazyLock::new(|| {
    vec![
        GreaseOrCipher::Grease,
        CipherSuite::TLS13_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS13_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS13_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA.into(),
        CipherSuite::TLS_RSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_RSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_RSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite::TLS_RSA_WITH_AES_256_CBC_SHA.into(),
    ]
});

define_fingerprint!(CHROME_108 { &CHROME_108_EXT, &CHROME_CIPHER });
define_fingerprint!(CHROME_112 { shuffle(&CHROME_108_EXT), &CHROME_CIPHER });
define_fingerprint!(RUSTLS_TEST { &EXT_TEST, &CHROME_CIPHER });

/// The cipher list of Safari 17.1
///
/// This list includes \*CBC* and \*CipherSuite::TLS_RSA* ciphers for correctness, even though they are not supported by Rustls due to security concerns and deprecation. As these older cipher suites are seldom used in modern secure communications, their absence in Rustls is unlikely to cause compatibility issues.
pub static SAFARI_17_1_CIPHERS: LazyLock<Vec<GreaseOrCipher>> = LazyLock::new(|| {
    vec![
        GreaseOrCipher::Grease,
        CipherSuite::TLS13_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS13_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS13_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite::TLS_RSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_RSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_RSA_WITH_AES_256_CBC_SHA.into(),
        CipherSuite::TLS_RSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite(0xc008).into(),
        CipherSuite(0xc012).into(),
        CipherSuite(0x000a).into(),
    ]
});

/// The signature algorithm list of Safari 17.1
pub static SAFARI_17_1_SIGNATURE_ALGO: &[SignatureScheme] = &[
    SignatureScheme::ECDSA_NISTP256_SHA256,
    SignatureScheme::RSA_PSS_SHA256,
    SignatureScheme::RSA_PKCS1_SHA256,
    SignatureScheme::ECDSA_NISTP384_SHA384,
    SignatureScheme::ECDSA_SHA1_Legacy,
    SignatureScheme::RSA_PSS_SHA384,
    SignatureScheme::RSA_PSS_SHA384,
    SignatureScheme::RSA_PKCS1_SHA384,
    SignatureScheme::RSA_PSS_SHA512,
    SignatureScheme::RSA_PKCS1_SHA512,
    SignatureScheme::RSA_PKCS1_SHA1,
];

/// The extension list of Safari 17.1
pub static SAFARI_17_1_EXT: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Craft(CraftExtension::Grease1),
        Keep(Must(ExtensionType::ServerName)),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::SupportedCurves(static_ref!(
            &[
                Grease,
                GreaseOrCurve::T(NamedGroup::X25519),
                GreaseOrCurve::T(NamedGroup::secp256r1),
                GreaseOrCurve::T(NamedGroup::secp384r1),
                GreaseOrCurve::T(NamedGroup::secp521r1),
            ],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Craft(CraftExtension::Protocols(&[b"h2", b"http/1.1"])),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Rustls(ClientExtension::SignatureAlgorithms(
            SAFARI_17_1_SIGNATURE_ALGO.to_vec(),
        )),
        Craft(CraftExtension::SignedCertificateTimestamp),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[Grease, GreaseOrCurve::T(NamedGroup::X25519)],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::PresharedKeyModes(vec![
            PSKKeyExchangeMode::PSK_DHE_KE,
        ])),
        Craft(CraftExtension::SupportedVersions(static_ref!(
            &[
                Grease,
                GreaseOrVersion::T(ProtocolVersion::TLSv1_3),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_2),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_1),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_0),
            ],
            &[GreaseOrVersion]
        ))),
        Keep(Optional(ExtensionType::Cookie)),
        Craft(CraftExtension::CompressCert(&[
            CertificateCompressionAlgorithm::Zlib,
        ])),
        Craft(CraftExtension::Grease2),
        Craft(CraftExtension::Padding),
    ]
});

define_fingerprint!(SAFARI_17_1 { &SAFARI_17_1_EXT, &SAFARI_17_1_CIPHERS });

/// The cipher list of firefox 105
///
/// This list includes \*CBC* and \*CipherSuite::TLS_RSA* ciphers for correctness, even though they are not supported by Rustls due to security concerns and deprecation. As these older cipher suites are seldom used in modern secure communications, their absence in Rustls is unlikely to cause compatibility issues.
pub static FIREFOX_105_CIPHERS: LazyLock<Vec<GreaseOrCipher>> = LazyLock::new(|| {
    vec![
        CipherSuite::TLS13_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS13_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS13_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA.into(),
        CipherSuite::TLS_RSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_RSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_RSA_WITH_AES_128_CBC_SHA.into(),
        CipherSuite::TLS_RSA_WITH_AES_256_CBC_SHA.into(),
    ]
});

/// The signature algorithm list of firefox 105
pub static FIREFOX_105_SIGNATURE_ALGO: &[SignatureScheme] = &[
    SignatureScheme::ECDSA_NISTP256_SHA256,
    SignatureScheme::ECDSA_NISTP384_SHA384,
    SignatureScheme::ECDSA_NISTP521_SHA512,
    SignatureScheme::RSA_PSS_SHA256,
    SignatureScheme::RSA_PSS_SHA384,
    SignatureScheme::RSA_PSS_SHA512,
    SignatureScheme::RSA_PKCS1_SHA256,
    SignatureScheme::RSA_PKCS1_SHA384,
    SignatureScheme::RSA_PKCS1_SHA512,
    SignatureScheme::ECDSA_SHA1_Legacy,
    SignatureScheme::RSA_PKCS1_SHA1,
];

/// The extension list of firefox 105
pub static FIREFOX_105_EXT: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Keep(Must(ExtensionType::ServerName)),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::SupportedCurves(static_ref!(
            &[
                GreaseOrCurve::T(NamedGroup::X25519),
                GreaseOrCurve::T(NamedGroup::secp256r1),
                GreaseOrCurve::T(NamedGroup::secp384r1),
                GreaseOrCurve::T(NamedGroup::secp521r1),
                GreaseOrCurve::T(NamedGroup::FFDHE2048),
                GreaseOrCurve::T(NamedGroup::FFDHE3072),
            ],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Craft(CraftExtension::Protocols(&[b"h2", b"http/1.1"])),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Craft(CraftExtension::FakeDelegatedCredentials(&[
            SignatureScheme::ECDSA_NISTP256_SHA256,
            SignatureScheme::ECDSA_NISTP384_SHA384,
            SignatureScheme::ECDSA_NISTP521_SHA512,
            SignatureScheme::ECDSA_SHA1_Legacy,
        ])),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[
                GreaseOrCurve::T(NamedGroup::X25519),
                GreaseOrCurve::T(NamedGroup::secp256r1),
            ],
            &[GreaseOrCurve]
        ))),
        Craft(CraftExtension::SupportedVersions(static_ref!(
            &[
                GreaseOrVersion::T(ProtocolVersion::TLSv1_3),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_2),
            ],
            &[GreaseOrVersion]
        ))),
        Rustls(ClientExtension::SignatureAlgorithms(
            FIREFOX_105_SIGNATURE_ALGO.to_vec(),
        )),
        Rustls(ClientExtension::PresharedKeyModes(vec![
            PSKKeyExchangeMode::PSK_DHE_KE,
        ])),
        Craft(CraftExtension::FakeRecordSizeLimit(0x4001)),
        Craft(CraftExtension::Padding),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

define_fingerprint!(FIREFOX_105 { &FIREFOX_105_EXT, &FIREFOX_105_CIPHERS });
