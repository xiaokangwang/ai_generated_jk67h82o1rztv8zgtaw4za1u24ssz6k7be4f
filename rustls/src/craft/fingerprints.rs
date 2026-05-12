#![allow(missing_docs)]

use super::*;
use crate::crypto::hpke::HpkeAead;
use std::sync::LazyLock;

macro_rules! define_fingerprint {
    ($fingerprint_name:ident { $extensions:expr, $cipher:expr, ech_force_tls13: $ech_force_tls13:expr, ech_padding: $ech_padding:expr }) => {
        define_fingerprint!(
            $fingerprint_name,
            $extensions,
            false,
            $cipher,
            Some($ech_force_tls13),
            $ech_padding
        );
    };
    ($fingerprint_name:ident { shuffle($extensions:expr), $cipher:expr, ech_force_tls13: $ech_force_tls13:expr }) => {
        define_fingerprint!(
            $fingerprint_name,
            $extensions,
            true,
            $cipher,
            Some($ech_force_tls13),
            EchPaddingStyle::Standard
        );
    };
    ($fingerprint_name:ident { $extensions:expr, $cipher:expr, ech_force_tls13: $ech_force_tls13:expr }) => {
        define_fingerprint!(
            $fingerprint_name,
            $extensions,
            false,
            $cipher,
            Some($ech_force_tls13),
            EchPaddingStyle::Standard
        );
    };
    ($fingerprint_name:ident { shuffle($extensions:expr), $cipher:expr }) => {
        define_fingerprint!(
            $fingerprint_name,
            $extensions,
            true,
            $cipher,
            None,
            EchPaddingStyle::Standard
        );
    };
    ($fingerprint_name:ident { $extensions:expr, $cipher:expr }) => {
        define_fingerprint!(
            $fingerprint_name,
            $extensions,
            false,
            $cipher,
            None,
            EchPaddingStyle::Standard
        );
    };
    ($fingerprint_name:ident, $extensions:expr, $shuffle_extensions:expr, $cipher:expr, $ech_force_tls13:expr, $ech_padding:expr) => {
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
                    ech_force_tls13: $ech_force_tls13,
                    ech_padding_style: $ech_padding,
                },
                test_alpn_http1: Fingerprint {
                    extensions: Box::leak(alpn_http1),
                    cipher: ($cipher).as_slice(),
                    shuffle_extensions: $shuffle_extensions,
                    ech_force_tls13: $ech_force_tls13,
                    ech_padding_style: $ech_padding,
                },
                test_no_alpn: Fingerprint {
                    extensions: Box::leak(no_alpn),
                    cipher: ($cipher).as_slice(),
                    shuffle_extensions: $shuffle_extensions,
                    ech_force_tls13: $ech_force_tls13,
                    ech_padding_style: $ech_padding,
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
        Craft(CraftExtension::ApplicationSettings {
            codepoint: ApplicationSettingsCodepoint::Old,
            protocols: &[b"h2"],
        }),
        Craft(CraftExtension::Grease2),
        Craft(CraftExtension::Padding),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

/// The extension list of chromium 144, captured from a no-SNI ECH-capable
/// ClientHello with h2/http1 ALPN and ALPS-new.
pub static CHROMIUM_144_EXT: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Craft(CraftExtension::Grease1),
        Keep(Must(ExtensionType::ServerName)),
        Craft(CraftExtension::Protocols(&[b"h2", b"http/1.1"])),
        Craft(CraftExtension::RenegotiationInfo),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Craft(CraftExtension::EchPlaceholder {
            payload_body_len: 1366,
            random_config_id: false,
            aead: EchPlaceholderAead::Fixed(HpkeAead::AES_128_GCM),
        }),
        Craft(CraftExtension::SupportedVersions(static_ref!(
            &[
                Grease,
                GreaseOrVersion::T(ProtocolVersion::TLSv1_3),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_2),
            ],
            &[GreaseOrVersion]
        ))),
        Keep(OrDefault(
            ExtensionType::SessionTicket,
            default_rustls_session_ticket(),
        )),
        Rustls(ClientExtension::PresharedKeyModes(vec![
            PSKKeyExchangeMode::PSK_DHE_KE,
        ])),
        Craft(CraftExtension::SignedCertificateTimestamp),
        Craft(CraftExtension::ApplicationSettings {
            codepoint: ApplicationSettingsCodepoint::New,
            protocols: &[b"h2"],
        }),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Rustls(ClientExtension::SignatureAlgorithms(
            CHROME_108_SIGNATURE_ALGO.to_vec(),
        )),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[Grease, GreaseOrCurve::T(NamedGroup::X25519),],
            &[GreaseOrCurve]
        ))),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Craft(CraftExtension::CompressCert(static_ref!(
            &[CertificateCompressionAlgorithm::Brotli],
            &[CertificateCompressionAlgorithm]
        ))),
        Craft(CraftExtension::SupportedCurves(static_ref!(
            &[
                Grease,
                GreaseOrCurve::T(NamedGroup::X25519),
                GreaseOrCurve::T(NamedGroup::secp256r1),
                GreaseOrCurve::T(NamedGroup::secp384r1),
            ],
            &[GreaseOrCurve]
        ))),
        Craft(CraftExtension::Grease2),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

/// The extension list of Chrome 148, captured from no-SNI ECH-capable
/// ClientHellos with h2/http1 ALPN, ALPS-new, and BoringSSL GREASE ECH.
pub static CHROME_148_EXT: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Craft(CraftExtension::Grease1),
        Keep(Must(ExtensionType::ServerName)),
        Craft(CraftExtension::Protocols(&[b"h2", b"http/1.1"])),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[
                Grease,
                GreaseOrCurve::T(NamedGroup::X25519MLKEM768),
                GreaseOrCurve::T(NamedGroup::X25519),
            ],
            &[GreaseOrCurve]
        ))),
        Craft(CraftExtension::ApplicationSettings {
            codepoint: ApplicationSettingsCodepoint::New,
            protocols: &[b"h2"],
        }),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Rustls(ClientExtension::SignatureAlgorithms(
            CHROME_108_SIGNATURE_ALGO.to_vec(),
        )),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Craft(CraftExtension::SupportedVersions(static_ref!(
            &[
                Grease,
                GreaseOrVersion::T(ProtocolVersion::TLSv1_3),
                GreaseOrVersion::T(ProtocolVersion::TLSv1_2),
            ],
            &[GreaseOrVersion]
        ))),
        Craft(CraftExtension::CompressCert(static_ref!(
            &[CertificateCompressionAlgorithm::Brotli],
            &[CertificateCompressionAlgorithm]
        ))),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Keep(OrDefault(
            ExtensionType::SessionTicket,
            default_rustls_session_ticket(),
        )),
        Rustls(ClientExtension::PresharedKeyModes(vec![
            PSKKeyExchangeMode::PSK_DHE_KE,
        ])),
        Craft(CraftExtension::SupportedCurves(static_ref!(
            &[
                Grease,
                GreaseOrCurve::T(NamedGroup::X25519MLKEM768),
                GreaseOrCurve::T(NamedGroup::X25519),
                GreaseOrCurve::T(NamedGroup::secp256r1),
                GreaseOrCurve::T(NamedGroup::secp384r1),
            ],
            &[GreaseOrCurve]
        ))),
        Craft(CraftExtension::SignedCertificateTimestamp),
        Craft(CraftExtension::BoringSslEchGrease {
            aead: HpkeAead::AES_128_GCM,
        }),
        Craft(CraftExtension::Grease2),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

/// A maximal craftls fingerprint that advertises the broadest extension surface
/// that remains appropriate for ordinary HTTPS-over-TLS handshakes.
///
/// This is useful for stress-testing server tolerance without sending extensions
/// which are QUIC-only, DTLS-only, PAKE-specific, or likely to require follow-up
/// handshake messages that craftls does not currently synthesize.
pub static CRAFTLSMAXXING_SIGNATURE_ALGO: LazyLock<Vec<GreaseOrSignatureScheme>> =
    LazyLock::new(|| {
        vec![
            Grease,
            SignatureScheme::ECDSA_NISTP256_SHA256.into(),
            SignatureScheme::ECDSA_NISTP384_SHA384.into(),
            SignatureScheme::ECDSA_NISTP521_SHA512.into(),
            SignatureScheme::RSA_PSS_SHA256.into(),
            SignatureScheme::RSA_PSS_SHA384.into(),
            SignatureScheme::RSA_PSS_SHA512.into(),
            SignatureScheme::RSA_PKCS1_SHA256.into(),
            SignatureScheme::RSA_PKCS1_SHA384.into(),
            SignatureScheme::RSA_PKCS1_SHA512.into(),
            SignatureScheme::ED25519.into(),
            SignatureScheme::ED448.into(),
            SignatureScheme::SM2_SM3.into(),
            SignatureScheme::ML_DSA_44.into(),
            SignatureScheme::ML_DSA_65.into(),
            SignatureScheme::ML_DSA_87.into(),
            SignatureScheme::ECDSA_SHA1_Legacy.into(),
            SignatureScheme::RSA_PKCS1_SHA1.into(),
        ]
    });

/// The broadest key exchange group list craftls can currently advertise using
/// the default aws-lc provider plus craftls' built-in FFDHE support.
pub static CRAFTLSMAXXING_GROUPS: LazyLock<Vec<GreaseOrCurve>> = LazyLock::new(|| {
    vec![
        Grease,
        NamedGroup::X25519MLKEM768.into(),
        NamedGroup::X25519.into(),
        NamedGroup::secp256r1.into(),
        NamedGroup::secp384r1.into(),
        NamedGroup::secp521r1.into(),
        NamedGroup::FFDHE2048.into(),
        NamedGroup::FFDHE3072.into(),
        NamedGroup::FFDHE4096.into(),
        NamedGroup::FFDHE6144.into(),
        NamedGroup::FFDHE8192.into(),
    ]
});

/// All cipher suites with implementations in the rustls ring/aws-lc providers,
/// plus a GREASE value.
pub static CRAFTLSMAXXING_CIPHER: LazyLock<Vec<GreaseOrCipher>> = LazyLock::new(|| {
    vec![
        GreaseOrCipher::Grease,
        CipherSuite::TLS13_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS13_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS13_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384.into(),
        CipherSuite::TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256.into(),
    ]
});

pub static CRAFTLSMAXXING_EXT: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Craft(CraftExtension::Grease1),
        Keep(Must(ExtensionType::ServerName)),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::SupportedCurves(&CRAFTLSMAXXING_GROUPS)),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Keep(OrDefault(
            ExtensionType::SessionTicket,
            default_rustls_session_ticket(),
        )),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Craft(CraftExtension::SignatureAlgorithms(
            &CRAFTLSMAXXING_SIGNATURE_ALGO,
        )),
        Craft(CraftExtension::SignatureAlgorithmsCert(
            &CRAFTLSMAXXING_SIGNATURE_ALGO,
        )),
        Craft(CraftExtension::ProtocolsWithGrease(static_ref!(
            &[
                GreaseOrProtocol::Grease,
                GreaseOrProtocol::Protocol(b"h2"),
                GreaseOrProtocol::Protocol(b"http/1.1"),
            ],
            &[GreaseOrProtocol]
        ))),
        Craft(CraftExtension::SignedCertificateTimestamp),
        Craft(CraftExtension::DelegatedCredentials(
            &CRAFTLSMAXXING_SIGNATURE_ALGO,
        )),
        Craft(CraftExtension::KeyShare(&CRAFTLSMAXXING_GROUPS)),
        Craft(CraftExtension::PresharedKeyModes(static_ref!(
            &[
                GreaseOrPskKeyExchangeMode::Grease,
                GreaseOrPskKeyExchangeMode::T(PSKKeyExchangeMode::PSK_DHE_KE),
            ],
            &[GreaseOrPskKeyExchangeMode]
        ))),
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
        Craft(CraftExtension::CompressCert(static_ref!(
            &[
                CertificateCompressionAlgorithm::Zlib,
                CertificateCompressionAlgorithm::Brotli,
                CertificateCompressionAlgorithm::Zstd,
            ],
            &[CertificateCompressionAlgorithm]
        ))),
        Craft(CraftExtension::BoringSslEchGrease {
            aead: HpkeAead::AES_128_GCM,
        }),
        Craft(CraftExtension::RecordSizeLimit(0x4001)),
        Craft(CraftExtension::PostHandshakeAuth),
        Craft(CraftExtension::RandomPrintableCertificateAuthorities {
            count: 16,
            name_len: 32,
        }),
        Craft(CraftExtension::ApplicationSettings {
            codepoint: ApplicationSettingsCodepoint::Old,
            protocols: &[b"h2"],
        }),
        Craft(CraftExtension::ApplicationSettings {
            codepoint: ApplicationSettingsCodepoint::New,
            protocols: &[b"h2"],
        }),
        Keep(Optional(ExtensionType::EarlyData)),
        Keep(Optional(ExtensionType::Cookie)),
        Craft(CraftExtension::Grease2),
        Craft(CraftExtension::Raw(ExtensionType::Padding, &[])),
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
define_fingerprint!(CHROMIUM_144 { shuffle(&CHROMIUM_144_EXT), &CHROME_CIPHER });
define_fingerprint!(CHROME_148 { shuffle(&CHROME_148_EXT), &CHROME_CIPHER });
define_fingerprint!(CRAFTLSMAXXING {
    shuffle(&CRAFTLSMAXXING_EXT),
    &CRAFTLSMAXXING_CIPHER,
    ech_force_tls13: false
});
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
                GreaseOrCurve::T(NamedGroup::X25519MLKEM768),
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
        Craft(CraftExtension::DelegatedCredentials(static_ref!(
            &[
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_NISTP256_SHA256),
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_NISTP384_SHA384),
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_NISTP521_SHA512),
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_SHA1_Legacy),
            ],
            &[GreaseOrSignatureScheme]
        ))),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[
                GreaseOrCurve::T(NamedGroup::X25519MLKEM768),
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
        Craft(CraftExtension::RecordSizeLimit(0x4001)),
        Craft(CraftExtension::Padding),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

define_fingerprint!(FIREFOX_105 { &FIREFOX_105_EXT, &FIREFOX_105_CIPHERS });

/// The extension list of firefox 140, captured from a no-SNI ECH-capable
/// ClientHello with h2/http1 ALPN.
pub static FIREFOX_140_EXT: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Keep(Must(ExtensionType::ServerName)),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::SupportedCurves(static_ref!(
            &[
                GreaseOrCurve::T(NamedGroup::X25519MLKEM768),
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
        Keep(OrDefault(
            ExtensionType::SessionTicket,
            default_rustls_session_ticket(),
        )),
        Craft(CraftExtension::Protocols(&[b"h2", b"http/1.1"])),
        Rustls(ClientExtension::CertificateStatusRequest(ocsp_req())),
        Craft(CraftExtension::DelegatedCredentials(static_ref!(
            &[
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_NISTP256_SHA256),
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_NISTP384_SHA384),
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_NISTP521_SHA512),
                GreaseOrSignatureScheme::T(SignatureScheme::ECDSA_SHA1_Legacy),
            ],
            &[GreaseOrSignatureScheme]
        ))),
        Craft(CraftExtension::SignedCertificateTimestamp),
        Craft(CraftExtension::KeyShare(static_ref!(
            &[
                GreaseOrCurve::T(NamedGroup::X25519MLKEM768),
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
        Craft(CraftExtension::RecordSizeLimit(0x4001)),
        Craft(CraftExtension::CompressCert(&[
            CertificateCompressionAlgorithm::Zlib,
            CertificateCompressionAlgorithm::Brotli,
            CertificateCompressionAlgorithm::Zstd,
        ])),
        // Sized with the larger hybrid key share to match the captured
        // no-SNI ClientHello length of 1871 bytes.
        Craft(CraftExtension::EchPlaceholder {
            payload_body_len: 239,
            random_config_id: true,
            aead: EchPlaceholderAead::NssGrease,
        }),
        Keep(Optional(ExtensionType::PreSharedKey)),
    ]
});

define_fingerprint!(FIREFOX_140 {
    &FIREFOX_140_EXT,
    &FIREFOX_105_CIPHERS,
    ech_force_tls13: false,
    ech_padding: EchPaddingStyle::Nss
});
