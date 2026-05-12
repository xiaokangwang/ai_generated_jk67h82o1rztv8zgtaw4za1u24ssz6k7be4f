use alloc::borrow::Cow;
use alloc::boxed::Box;
use alloc::vec::Vec;
use core::hash::Hasher;
use core::sync::atomic::{AtomicBool, AtomicU8, Ordering};
use core::time::Duration;
use std::io::{Read, Write};
use std::sync::OnceLock;
use std::vec;

use pki_types::{CertificateDer, DnsName, FipsStatus, ServerName, UnixTime};

use super::{Tls12Session, Tls13ClientSessionInput, Tls13Session};
use crate::client::{ClientConfig, EchConfig, EchMode, EchStatus, Resumption, Tls12Resumption};
use crate::crypto::cipher::{EncodedMessage, MessageEncrypter, Payload};
use crate::crypto::hpke::{
    EncapsulatedSecret, Hpke, HpkeAead, HpkeKdf, HpkeKem, HpkePrivateKey, HpkePublicKey,
    HpkeSealer, HpkeSuite, HpkeSymmetricCipherSuite,
};
use crate::crypto::kx::{self, NamedGroup, SharedSecret, StartedKeyExchange, SupportedKxGroup};
use crate::crypto::test_provider::FakeKeyExchangeGroup;
use crate::crypto::tls13::OkmBlock;
use crate::crypto::{
    CipherSuite, Credentials, CryptoProvider, GetRandomFailed, Identity, SecureRandom,
    SignatureScheme, SingleCredential, TEST_PROVIDER, tls12_only, tls13_only, tls13_suite,
};
use crate::enums::{
    ApplicationProtocol, CertificateCompressionAlgorithm, CertificateType, ContentType,
    HandshakeType, ProtocolVersion,
};
use crate::error::{ApiMisuse, Error, PeerIncompatible, PeerMisbehaved};
use crate::msgs::{
    CertificateChain, ClientHelloPayload, Codec, Compression, ECCurveType, EcParameters,
    EchConfigContents, EchConfigPayload, ExtensionType, HandshakeMessagePayload, HandshakePayload,
    HelloRetryRequest, HelloRetryRequestExtensions, HpkeKeyConfig, KeyShareEntry, MaybeEmpty,
    Message, MessagePayload, NewSessionTicketExtensions, NewSessionTicketPayloadTls13, Random,
    Reader, ServerEcdhParams, ServerExtensions, ServerHelloPayload, ServerKeyExchange,
    ServerKeyExchangeParams, ServerKeyExchangePayload, SessionId, SizedPayload,
};
use crate::pki_types::PrivateKeyDer;
use crate::pki_types::pem::PemObject;
use crate::server::{NoServerSessionStorage, ServerConfig, ServerConnection};
use crate::sync::Arc;
use crate::tls13::key_schedule::{derive_traffic_iv, derive_traffic_key};
use crate::verify::{
    HandshakeSignatureValid, PeerVerified, ServerIdentity, ServerVerifier,
    SignatureVerificationInput,
};
use crate::{Connection, DigitallySignedStruct, DistinguishedName, KeyLog, RootCertStore};

#[test]
fn tls12_client_session_value_roundtrip() {
    let session_id = SessionId::read(&mut Reader::new(&[
        32, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e,
        0x0f, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d,
        0x1e, 0x1f, 0x20,
    ]))
    .unwrap();

    let peer_identity = Identity::X509(crate::crypto::CertificateIdentity {
        end_entity: CertificateDer::from(&b"test cert"[..]),
        intermediates: vec![],
    });

    let session = Tls12Session::new(
        TEST_PROVIDER.tls12_cipher_suites[0],
        session_id,
        Arc::new(SizedPayload::from(vec![0xde, 0xad, 0xbe, 0xef])),
        &[0xab; 48],
        peer_identity.clone(),
        UnixTime::since_unix_epoch(Duration::from_secs(1234567890)),
        Duration::from_secs(3600),
        true, // extended_ms
    );

    let mut encoded = Vec::new();
    session.encode(&mut encoded);
    let decoded = Tls12Session::from_slice(&encoded, &TEST_PROVIDER).unwrap();

    assert_eq!(decoded.suite.common.suite, session.suite.common.suite);
    assert_eq!(decoded.session_id, session_id);
    assert_eq!(&*decoded.master_secret, &*session.master_secret);
    assert_eq!(decoded.extended_ms, session.extended_ms);
    assert_eq!(decoded.common.ticket(), session.common.ticket());
    assert_eq!(decoded.common.epoch, session.common.epoch);
    assert_eq!(*decoded.common.peer_identity(), peer_identity);
}

#[test]
fn tls13_client_session_value_roundtrip() {
    let age_add = 0x12345678_u32;
    let peer_identity = Identity::RawPublicKey(pki_types::SubjectPublicKeyInfoDer::from(
        &b"raw public key"[..],
    ));

    let session = Tls13Session::new(
        &NewSessionTicketPayloadTls13 {
            lifetime: Duration::from_secs(1800),
            age_add,
            nonce: SizedPayload::empty(),
            ticket: Arc::new(SizedPayload::from(vec![0x11, 0x22, 0x33])),
            extensions: NewSessionTicketExtensions {
                max_early_data_size: Some(8192),
            },
        },
        Tls13ClientSessionInput {
            suite: TEST_PROVIDER.tls13_cipher_suites[0],
            peer_identity: peer_identity.clone(),
            quic_params: Some(SizedPayload::<u16, MaybeEmpty>::from(vec![
                0xaa, 0xbb, 0xcc, 0xdd,
            ])),
        },
        &[0x55; 48],
        UnixTime::since_unix_epoch(Duration::from_secs(9999999)),
    );

    let mut encoded = Vec::new();
    session.encode(&mut encoded);
    let decoded = Tls13Session::from_slice(&encoded, &TEST_PROVIDER).unwrap();

    assert_eq!(decoded.suite.common.suite, session.suite.common.suite);
    assert_eq!(decoded.secret.bytes(), session.secret.bytes());
    assert_eq!(decoded.age_add, age_add);
    assert_eq!(decoded.max_early_data_size, session.max_early_data_size);
    assert_eq!(decoded.quic_params.bytes(), session.quic_params.bytes());
    assert_eq!(decoded.common.ticket(), session.common.ticket());
    assert_eq!(decoded.common.epoch, session.common.epoch);
    assert_eq!(*decoded.common.peer_identity(), peer_identity);
}

/// Tests that session_ticket(35) extension
/// is not sent if the client does not support TLS 1.2.
#[test]
fn test_no_session_ticket_request_on_tls_1_3() {
    let mut config = ClientConfig::builder(Arc::new(tls13_only(TEST_PROVIDER.clone())))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap();
    config.resumption =
        Resumption::in_memory_sessions(128).tls12_resumption(Tls12Resumption::SessionIdOrTickets);
    let ch = client_hello_sent_for_config(config).unwrap();
    assert!(ch.extensions.session_ticket.is_none());
}

#[test]
fn test_no_renegotiation_scsv_on_tls_1_3() {
    let ch = client_hello_sent_for_config(
        ClientConfig::builder(Arc::new(tls13_only(TEST_PROVIDER.clone())))
            .with_root_certificates(roots())
            .with_no_client_auth()
            .unwrap(),
    )
    .unwrap();
    assert!(
        !ch.cipher_suites
            .contains(&CipherSuite::TLS_EMPTY_RENEGOTIATION_INFO_SCSV)
    );
}

#[test]
fn test_client_does_not_offer_sha1() {
    for provider in [
        tls12_only(TEST_PROVIDER.clone()),
        tls13_only(TEST_PROVIDER.clone()),
    ] {
        let config = ClientConfig::builder(Arc::new(provider))
            .with_root_certificates(roots())
            .with_no_client_auth()
            .unwrap();
        let ch = client_hello_sent_for_config(config).unwrap();
        assert!(
            !ch.extensions
                .signature_schemes
                .as_ref()
                .unwrap()
                .contains(&SignatureScheme::RSA_PKCS1_SHA1),
            "sha1 unexpectedly offered"
        );
    }
}

#[test]
fn test_client_rejects_hrr_with_varied_session_id() {
    let config = ClientConfig::builder(Arc::new(TEST_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap();
    let mut conn = Arc::new(config)
        .connect(ServerName::try_from("localhost").unwrap())
        .build()
        .unwrap();
    let mut sent = Vec::new();
    conn.write_tls(&mut sent).unwrap();

    // server replies with HRR, but does not echo `session_id` as required.
    let hrr = Message {
        version: ProtocolVersion::TLSv1_3,
        payload: MessagePayload::handshake(HandshakeMessagePayload(
            HandshakePayload::HelloRetryRequest(HelloRetryRequest {
                cipher_suite: CipherSuite::TLS13_AES_128_GCM_SHA256,
                legacy_version: ProtocolVersion::TLSv1_2,
                session_id: SessionId::empty(),
                extensions: HelloRetryRequestExtensions {
                    cookie: Some(SizedPayload::from(vec![1, 2, 3, 4])),
                    ..HelloRetryRequestExtensions::default()
                },
            }),
        )),
    };

    conn.read_tls(&mut hrr.into_wire_bytes().as_slice())
        .unwrap();
    assert_eq!(
        conn.process_new_packets().unwrap_err(),
        PeerMisbehaved::IllegalHelloRetryRequestWithWrongSessionId.into()
    );
}

#[test]
fn test_client_rejects_no_extended_master_secret_extension_when_require_ems_or_fips() {
    let mut config = ClientConfig::builder(Arc::new(TEST_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap();
    if !matches!(config.provider().fips(), FipsStatus::Unvalidated) {
        assert!(config.require_ems);
    } else {
        config.require_ems = true;
    }

    let config = Arc::new(config);
    let mut conn = config
        .connect(ServerName::try_from("localhost").unwrap())
        .build()
        .unwrap();
    let mut sent = Vec::new();
    conn.write_tls(&mut sent).unwrap();

    let sh = Message {
        version: ProtocolVersion::TLSv1_3,
        payload: MessagePayload::handshake(HandshakeMessagePayload(HandshakePayload::ServerHello(
            ServerHelloPayload {
                random: Random::new(config.provider().secure_random).unwrap(),
                compression_method: Compression::Null,
                cipher_suite: CipherSuite(0xff12),
                legacy_version: ProtocolVersion::TLSv1_2,
                session_id: SessionId::empty(),
                extensions: Box::new(ServerExtensions::default()),
            },
        ))),
    };
    conn.read_tls(&mut sh.into_wire_bytes().as_slice())
        .unwrap();

    assert_eq!(
        conn.process_new_packets(),
        Err(PeerIncompatible::ExtendedMasterSecretExtensionRequired.into())
    );
}

#[test]
fn cas_extension_in_client_hello_if_server_verifier_requests_it() {
    let cas_sending_server_verifier =
        ServerVerifierWithAuthorityNames(Arc::from(vec![DistinguishedName::from(
            b"hello".to_vec(),
        )]));

    let tls12_provider = tls12_only(TEST_PROVIDER.clone());
    let tls13_provider = tls13_only(TEST_PROVIDER.clone());
    for (provider, cas_extension_expected) in [(tls12_provider, false), (tls13_provider, true)] {
        let client_hello = client_hello_sent_for_config(
            ClientConfig::builder(provider.into())
                .dangerous()
                .with_custom_certificate_verifier(Arc::new(cas_sending_server_verifier.clone()))
                .with_no_client_auth()
                .unwrap(),
        )
        .unwrap();
        assert_eq!(
            client_hello
                .extensions
                .certificate_authority_names
                .is_some(),
            cas_extension_expected
        );
    }
}

/// Regression test for <https://github.com/seanmonstar/reqwest/issues/2191>
#[test]
fn test_client_with_custom_verifier_can_accept_ecdsa_sha1_signatures() {
    let Some(provider) = x25519_provider(TEST_PROVIDER.clone()) else {
        return;
    };

    let verifier = Arc::new(ExpectSha1EcdsaVerifier::default());
    let config = ClientConfig::builder(Arc::new(provider))
        .dangerous()
        .with_custom_certificate_verifier(verifier.clone())
        .with_no_client_auth()
        .unwrap();

    let mut conn = Arc::new(config)
        .connect(ServerName::try_from("localhost").unwrap())
        .build()
        .unwrap();
    let mut sent = Vec::new();
    conn.write_tls(&mut sent).unwrap();

    let sh = Message {
        version: ProtocolVersion::TLSv1_2,
        payload: MessagePayload::handshake(HandshakeMessagePayload(HandshakePayload::ServerHello(
            ServerHelloPayload {
                random: Random([0u8; 32]),
                compression_method: Compression::Null,
                cipher_suite: CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
                legacy_version: ProtocolVersion::TLSv1_2,
                session_id: SessionId::empty(),
                extensions: Box::new(ServerExtensions {
                    extended_master_secret_ack: Some(()),
                    ..ServerExtensions::default()
                }),
            },
        ))),
    };
    conn.read_tls(&mut sh.into_wire_bytes().as_slice())
        .unwrap();
    conn.process_new_packets().unwrap();

    let cert = Message {
        version: ProtocolVersion::TLSv1_2,
        payload: MessagePayload::handshake(HandshakeMessagePayload(HandshakePayload::Certificate(
            CertificateChain(vec![CertificateDer::from(&b"does not matter"[..])]),
        ))),
    };
    conn.read_tls(&mut cert.into_wire_bytes().as_slice())
        .unwrap();
    conn.process_new_packets().unwrap();

    let server_kx = Message {
        version: ProtocolVersion::TLSv1_2,
        payload: MessagePayload::handshake(HandshakeMessagePayload(
            HandshakePayload::ServerKeyExchange(ServerKeyExchangePayload::Known(
                ServerKeyExchange {
                    dss: DigitallySignedStruct::new(
                        SignatureScheme::ECDSA_SHA1_Legacy,
                        b"also does not matter".to_vec(),
                    ),
                    params: ServerKeyExchangeParams::Ecdh(ServerEcdhParams {
                        curve_params: EcParameters {
                            curve_type: ECCurveType::NamedCurve,
                            named_group: NamedGroup::X25519,
                        },
                        public: SizedPayload::from(vec![0xab; 32]),
                    }),
                },
            )),
        )),
    };
    conn.read_tls(&mut server_kx.into_wire_bytes().as_slice())
        .unwrap();
    conn.process_new_packets().unwrap();

    let server_done = Message {
        version: ProtocolVersion::TLSv1_2,
        payload: MessagePayload::handshake(HandshakeMessagePayload(
            HandshakePayload::ServerHelloDone,
        )),
    };
    conn.read_tls(&mut server_done.into_wire_bytes().as_slice())
        .unwrap();
    conn.process_new_packets().unwrap();

    assert!(
        verifier
            .seen_sha1_signature
            .load(Ordering::SeqCst)
    );
}

#[derive(Debug, Default)]
struct ExpectSha1EcdsaVerifier {
    seen_sha1_signature: AtomicBool,
}

impl ServerVerifier for ExpectSha1EcdsaVerifier {
    fn verify_identity(&self, _identity: &ServerIdentity<'_>) -> Result<PeerVerified, Error> {
        Ok(PeerVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        assert_eq!(input.signature.scheme, SignatureScheme::ECDSA_SHA1_Legacy);
        self.seen_sha1_signature
            .store(true, Ordering::SeqCst);
        Ok(HandshakeSignatureValid::assertion())
    }

    #[cfg_attr(coverage_nightly, coverage(off))]
    fn verify_tls13_signature(
        &self,
        _input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        todo!()
    }

    fn request_ocsp_response(&self) -> bool {
        false
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        vec![SignatureScheme::ECDSA_SHA1_Legacy]
    }

    fn hash_config(&self, _: &mut dyn Hasher) {}
}

#[test]
fn test_client_requiring_rpk_rejects_server_that_only_offers_x509_id_by_omission() {
    client_requiring_rpk_receives_server_ee(
        Err(PeerIncompatible::IncorrectCertificateTypeExtension.into()),
        ServerExtensions::default(),
        &TEST_PROVIDER,
    );
}

#[test]
fn test_client_requiring_rpk_rejects_server_that_only_offers_x509_id() {
    client_requiring_rpk_receives_server_ee(
        Err(PeerIncompatible::IncorrectCertificateTypeExtension.into()),
        ServerExtensions {
            server_certificate_type: Some(CertificateType::X509),
            ..ServerExtensions::default()
        },
        &TEST_PROVIDER,
    );
}

#[test]
fn test_client_requiring_rpk_rejects_server_that_only_demands_x509_by_omission() {
    client_requiring_rpk_receives_server_ee(
        Err(PeerIncompatible::IncorrectCertificateTypeExtension.into()),
        ServerExtensions {
            server_certificate_type: Some(CertificateType::RawPublicKey),
            ..ServerExtensions::default()
        },
        &TEST_PROVIDER,
    );
}

#[test]
fn test_client_requiring_rpk_rejects_server_that_only_demands_x509() {
    client_requiring_rpk_receives_server_ee(
        Err(PeerIncompatible::IncorrectCertificateTypeExtension.into()),
        ServerExtensions {
            client_certificate_type: Some(CertificateType::X509),
            server_certificate_type: Some(CertificateType::RawPublicKey),
            ..ServerExtensions::default()
        },
        &TEST_PROVIDER,
    );
}

#[test]
fn test_client_requiring_rpk_accepts_rpk_server() {
    client_requiring_rpk_receives_server_ee(
        Ok(()),
        ServerExtensions {
            client_certificate_type: Some(CertificateType::RawPublicKey),
            server_certificate_type: Some(CertificateType::RawPublicKey),
            ..ServerExtensions::default()
        },
        &TEST_PROVIDER,
    );
}

#[track_caller]
fn client_requiring_rpk_receives_server_ee(
    expected: Result<(), Error>,
    encrypted_extensions: ServerExtensions<'_>,
    provider: &CryptoProvider,
) {
    let Some(provider) = x25519_provider(provider.clone()) else {
        return;
    };

    let provider = Arc::new(CryptoProvider {
        tls12_cipher_suites: Cow::default(),
        ..provider
    });

    let fake_server_crypto = Arc::new(FakeServerCrypto::new(provider.clone()));
    let credentials = client_credentials(&provider);
    let mut config = ClientConfig::builder(provider)
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(ServerVerifierRequiringRpk))
        .with_client_credential_resolver(Arc::new(SingleCredential::from(credentials)))
        .unwrap();
    config.key_log = fake_server_crypto.clone();

    let mut conn = Arc::new(config)
        .connect(ServerName::try_from("localhost").unwrap())
        .build()
        .unwrap();
    let mut sent = Vec::new();
    conn.write_tls(&mut sent).unwrap();

    let sh = Message {
        version: ProtocolVersion::TLSv1_3,
        payload: MessagePayload::handshake(HandshakeMessagePayload(HandshakePayload::ServerHello(
            ServerHelloPayload {
                random: Random([0; 32]),
                compression_method: Compression::Null,
                cipher_suite: CipherSuite::TLS13_AES_128_GCM_SHA256,
                legacy_version: ProtocolVersion::TLSv1_3,
                session_id: SessionId::empty(),
                extensions: Box::new(ServerExtensions {
                    key_share: Some(KeyShareEntry {
                        group: NamedGroup::X25519,
                        payload: SizedPayload::from(vec![0xaa; 32]),
                    }),
                    ..ServerExtensions::default()
                }),
            },
        ))),
    };
    conn.read_tls(&mut sh.into_wire_bytes().as_slice())
        .unwrap();
    conn.process_new_packets().unwrap();

    let ee = Message {
        version: ProtocolVersion::TLSv1_3,
        payload: MessagePayload::handshake(HandshakeMessagePayload(
            HandshakePayload::EncryptedExtensions(Box::new(encrypted_extensions)),
        )),
    };

    let mut encrypter = fake_server_crypto.server_handshake_encrypter();
    let enc_ee = encrypter
        .encrypt(EncodedMessage::<Payload<'_>>::from(ee).borrow_outbound(), 0)
        .unwrap();
    conn.read_tls(&mut enc_ee.encode().as_slice())
        .unwrap();

    assert_eq!(conn.process_new_packets().map(|_| ()), expected);
}

fn client_credentials(provider: &CryptoProvider) -> Credentials {
    let key = provider
        .key_provider
        .load_private_key(client_key())
        .unwrap();
    let identity = Arc::from(Identity::RawPublicKey(
        key.public_key().unwrap().into_owned(),
    ));
    Credentials::new_unchecked(identity, key)
}

fn client_key() -> PrivateKeyDer<'static> {
    PrivateKeyDer::from_pem_reader(
        &mut include_bytes!("../../../test-ca/rsa-2048/client.key").as_slice(),
    )
    .unwrap()
}

fn x25519_provider(provider: CryptoProvider) -> Option<CryptoProvider> {
    // ensures X25519 is offered irrespective of cfg(feature = "fips"), which eases
    // creation of fake server messages.
    let x25519 = provider.find_kx_group(NamedGroup::X25519, ProtocolVersion::TLSv1_3)?;
    Some(CryptoProvider {
        kx_groups: Cow::Owned(vec![x25519]),
        ..provider
    })
}

#[derive(Clone, Debug)]
struct ServerVerifierWithAuthorityNames(Arc<[DistinguishedName]>);

impl ServerVerifier for ServerVerifierWithAuthorityNames {
    fn root_hint_subjects(&self) -> Option<Arc<[DistinguishedName]>> {
        Some(self.0.clone())
    }

    #[cfg_attr(coverage_nightly, coverage(off))]
    fn verify_identity(&self, _identity: &ServerIdentity<'_>) -> Result<PeerVerified, Error> {
        unreachable!()
    }

    #[cfg_attr(coverage_nightly, coverage(off))]
    fn verify_tls12_signature(
        &self,
        _input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        unreachable!()
    }

    #[cfg_attr(coverage_nightly, coverage(off))]
    fn verify_tls13_signature(
        &self,
        _input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        unreachable!()
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        vec![SignatureScheme::RSA_PKCS1_SHA1]
    }

    fn request_ocsp_response(&self) -> bool {
        false
    }

    fn hash_config(&self, _: &mut dyn Hasher) {}
}

#[derive(Debug)]
struct ServerVerifierRequiringRpk;

impl ServerVerifier for ServerVerifierRequiringRpk {
    #[cfg_attr(coverage_nightly, coverage(off))]
    fn verify_identity(&self, _identity: &ServerIdentity<'_>) -> Result<PeerVerified, Error> {
        todo!()
    }

    #[cfg_attr(coverage_nightly, coverage(off))]
    fn verify_tls12_signature(
        &self,
        _input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        todo!()
    }

    #[cfg_attr(coverage_nightly, coverage(off))]
    fn verify_tls13_signature(
        &self,
        _input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        todo!()
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        vec![SignatureScheme::RSA_PKCS1_SHA1]
    }

    fn request_ocsp_response(&self) -> bool {
        false
    }

    fn supported_certificate_types(&self) -> &'static [CertificateType] {
        &[CertificateType::RawPublicKey]
    }

    fn hash_config(&self, _: &mut dyn Hasher) {}
}

#[derive(Debug)]
struct FakeServerCrypto {
    server_handshake_secret: OnceLock<Vec<u8>>,
    provider: Arc<CryptoProvider>,
}

impl FakeServerCrypto {
    fn new(provider: Arc<CryptoProvider>) -> Self {
        Self {
            server_handshake_secret: OnceLock::new(),
            provider,
        }
    }

    fn server_handshake_encrypter(&self) -> Box<dyn MessageEncrypter> {
        let secret = self
            .server_handshake_secret
            .get()
            .unwrap();

        let cipher_suite = tls13_suite(CipherSuite::TLS13_AES_128_GCM_SHA256, &self.provider);
        let expander = cipher_suite
            .hkdf_provider
            .expander_for_okm(&OkmBlock::new(secret));

        // Derive Encrypter
        let key = derive_traffic_key(expander.as_ref(), cipher_suite.aead_alg);
        let iv = derive_traffic_iv(expander.as_ref(), cipher_suite.aead_alg.iv_len());
        cipher_suite.aead_alg.encrypter(key, iv)
    }
}

impl KeyLog for FakeServerCrypto {
    fn will_log(&self, _label: &str) -> bool {
        true
    }

    fn log(&self, label: &str, _client_random: &[u8], secret: &[u8]) {
        if label == "SERVER_HANDSHAKE_TRAFFIC_SECRET" {
            self.server_handshake_secret
                .set(secret.to_vec())
                .unwrap();
        }
    }
}

// invalid with fips, as we can't offer X25519 separately
#[test]
fn hybrid_kx_component_share_offered_if_supported_separately() {
    let ch = client_hello_sent_for_config(
        ClientConfig::builder(Arc::new(HYBRID_PROVIDER.clone()))
            .with_root_certificates(roots())
            .with_no_client_auth()
            .unwrap(),
    )
    .unwrap();

    let key_shares = ch
        .extensions
        .key_shares
        .as_ref()
        .unwrap();
    assert_eq!(key_shares.len(), 2);
    assert_eq!(key_shares[0].group, NamedGroup(0xfe00));
    assert_eq!(key_shares[1].group, NamedGroup(0xfe01));
}

#[test]
fn hybrid_kx_component_share_not_offered_unless_supported_separately() {
    let provider = CryptoProvider {
        kx_groups: Cow::Owned(vec![FAKE_HYBRID as _]),
        ..HYBRID_PROVIDER
    };
    let ch = client_hello_sent_for_config(
        ClientConfig::builder(provider.into())
            .with_root_certificates(roots())
            .with_no_client_auth()
            .unwrap(),
    )
    .unwrap();

    let key_shares = ch
        .extensions
        .key_shares
        .as_ref()
        .unwrap();
    assert_eq!(key_shares.len(), 1);
    assert_eq!(key_shares[0].group, NamedGroup(0xfe00));
}

#[cfg(feature = "brotli")]
#[test]
fn craft_fingerprint_controls_client_hello_wire_image() {
    let config = ClientConfig::builder(Arc::new(CRAFT_CHROME_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(
            crate::craft::CHROME_108
                .test_alpn_http1
                .builder(),
        );
    let (decoded, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let raw = RawClientHello::parse(&wire);

    assert_eq!(raw.cipher_suites, decoded.cipher_suites);
    assert_chrome_108_cipher_suites(&raw.cipher_suites);

    let extension_types = raw
        .extensions
        .iter()
        .map(|extension| extension.typ)
        .collect::<Vec<_>>();
    assert_eq!(extension_types.len(), 18);
    assert!(is_grease_u16(u16::from(extension_types[0])));
    assert_eq!(
        &extension_types[1..16],
        &[
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
            ExtensionType::SupportedVersions,
            ExtensionType::CompressCertificate,
            ExtensionType(17513),
        ]
    );
    assert!(is_grease_u16(u16::from(extension_types[16])));
    assert_ne!(extension_types[0], extension_types[16]);
    assert_eq!(extension_types[17], ExtensionType::Padding);

    assert_eq!(
        raw.extension(ExtensionType::ALProtocolNegotiation),
        &[0, 9, 8, b'h', b't', b't', b'p', b'/', b'1', b'.', b'1']
    );
    assert_eq!(raw.extension(ExtensionType::ExtendedMasterSecret), &[]);
    assert_eq!(raw.extension(ExtensionType::RenegotiationInfo), &[0]);
    assert_eq!(raw.extension(ExtensionType::SessionTicket), &[]);
    assert_eq!(
        raw.extension(ExtensionType::CompressCertificate),
        &[2, 0, 2]
    );
    assert_eq!(raw.extension(ExtensionType(17513)), &[0, 3, 2, b'h', b'2']);

    let groups = decode_u16_list(raw.extension(ExtensionType::EllipticCurves))
        .into_iter()
        .map(NamedGroup)
        .collect::<Vec<_>>();
    assert_eq!(groups.len(), 4);
    assert!(is_grease_u16(u16::from(groups[0])));
    assert_eq!(
        &groups[1..],
        &[
            NamedGroup::X25519,
            NamedGroup::secp256r1,
            NamedGroup::secp384r1,
        ]
    );

    let versions = decode_u8_prefixed_u16_list(raw.extension(ExtensionType::SupportedVersions))
        .into_iter()
        .map(ProtocolVersion)
        .collect::<Vec<_>>();
    assert_eq!(versions.len(), 3);
    assert!(is_grease_u16(u16::from(versions[0])));
    assert_eq!(
        &versions[1..],
        &[ProtocolVersion::TLSv1_3, ProtocolVersion::TLSv1_2]
    );

    let key_shares = decode_key_shares(raw.extension(ExtensionType::KeyShare));
    assert_eq!(key_shares.len(), 2);
    assert!(is_grease_u16(u16::from(key_shares[0].0)));
    assert_eq!(key_shares[0].1, [0]);
    assert_eq!(key_shares[1].0, NamedGroup::X25519);
    assert_eq!(key_shares[1].1, KX_PEER_SHARE);
}

#[cfg(feature = "brotli")]
#[test]
fn craft_chrome_148_matches_captured_client_hello_shape() {
    let mut config = ClientConfig::builder(Arc::new(CRAFT_CHROME_148_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(crate::craft::CHROME_148.builder());
    config.enable_sni = false;

    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let raw = RawClientHello::parse(&wire);

    assert_chrome_108_cipher_suites(&raw.cipher_suites);

    let extension_types = raw
        .extensions
        .iter()
        .map(|extension| extension.typ)
        .collect::<Vec<_>>();
    assert_eq!(extension_types.len(), 17);
    assert!(is_grease_u16(u16::from(extension_types[0])));
    assert!(is_grease_u16(u16::from(extension_types[16])));
    assert_ne!(extension_types[0], extension_types[16]);
    assert_eq!(raw.extension(extension_types[16]), &[0]);
    assert!(!extension_types.contains(&ExtensionType::ServerName));

    let mut actual_extensions = extension_types[1..16]
        .iter()
        .map(|typ| u16::from(*typ))
        .collect::<Vec<_>>();
    actual_extensions.sort_unstable();
    let mut expected_extensions = [
        ExtensionType::ALProtocolNegotiation,
        ExtensionType::RenegotiationInfo,
        ExtensionType::KeyShare,
        ExtensionType::ApplicationSettings,
        ExtensionType::StatusRequest,
        ExtensionType::SignatureAlgorithms,
        ExtensionType::ECPointFormats,
        ExtensionType::SupportedVersions,
        ExtensionType::CompressCertificate,
        ExtensionType::ExtendedMasterSecret,
        ExtensionType::SessionTicket,
        ExtensionType::PSKKeyExchangeModes,
        ExtensionType::EllipticCurves,
        ExtensionType::SCT,
        ExtensionType::EncryptedClientHello,
    ]
    .into_iter()
    .map(u16::from)
    .collect::<Vec<_>>();
    expected_extensions.sort_unstable();
    assert_eq!(actual_extensions, expected_extensions);

    assert_eq!(
        raw.extension(ExtensionType::ALProtocolNegotiation),
        &[
            0, 12, 2, b'h', b'2', 8, b'h', b't', b't', b'p', b'/', b'1', b'.', b'1'
        ]
    );
    assert_eq!(
        raw.extension(ExtensionType::ApplicationSettings),
        &[0, 3, 2, b'h', b'2']
    );
    assert_eq!(
        raw.extension(ExtensionType::CompressCertificate),
        &[2, 0, 2]
    );
    assert_eq!(raw.extension(ExtensionType::SessionTicket), &[]);
    assert_eq!(raw.extension(ExtensionType::ExtendedMasterSecret), &[]);
    assert_eq!(raw.extension(ExtensionType::RenegotiationInfo), &[0]);
    assert_eq!(raw.extension(ExtensionType::SCT), &[]);

    let versions = decode_supported_versions(raw.extension(ExtensionType::SupportedVersions));
    assert_eq!(versions.len(), 3);
    assert!(is_grease_u16(u16::from(versions[0])));
    assert_eq!(
        &versions[1..],
        &[ProtocolVersion::TLSv1_3, ProtocolVersion::TLSv1_2]
    );

    let groups = decode_u16_list(raw.extension(ExtensionType::EllipticCurves))
        .into_iter()
        .map(NamedGroup)
        .collect::<Vec<_>>();
    assert_eq!(groups.len(), 5);
    assert!(is_grease_u16(u16::from(groups[0])));
    assert_eq!(
        &groups[1..],
        &[
            NamedGroup::X25519MLKEM768,
            NamedGroup::X25519,
            NamedGroup::secp256r1,
            NamedGroup::secp384r1,
        ]
    );

    let key_shares = decode_key_shares(raw.extension(ExtensionType::KeyShare));
    assert_eq!(key_shares.len(), 3);
    assert!(is_grease_u16(u16::from(key_shares[0].0)));
    assert_eq!(key_shares[0].1, [0]);
    assert_eq!(key_shares[1].0, NamedGroup::X25519MLKEM768);
    assert_eq!(key_shares[2].0, NamedGroup::X25519);

    let sigalgs = decode_u16_list(raw.extension(ExtensionType::SignatureAlgorithms));
    assert_eq!(
        sigalgs,
        [
            SignatureScheme::ECDSA_NISTP256_SHA256,
            SignatureScheme::RSA_PSS_SHA256,
            SignatureScheme::RSA_PKCS1_SHA256,
            SignatureScheme::ECDSA_NISTP384_SHA384,
            SignatureScheme::RSA_PSS_SHA384,
            SignatureScheme::RSA_PKCS1_SHA384,
            SignatureScheme::RSA_PSS_SHA512,
            SignatureScheme::RSA_PKCS1_SHA512,
        ]
        .into_iter()
        .map(u16::from)
        .collect::<Vec<_>>()
    );

    let (kdf, aead, _config_id, enc, encrypted_payload) =
        decode_ech_outer_payload(raw.extension(ExtensionType::EncryptedClientHello));
    assert_eq!(kdf, 1);
    assert_eq!(aead, HpkeAead::AES_128_GCM.0);
    assert_eq!(enc.len(), 32);
    assert!([144, 176, 208, 240].contains(&encrypted_payload.len()));
}

#[cfg(feature = "brotli")]
#[test]
fn craft_chrome_148_boringssl_ech_grease_varies_payload_size() {
    CHROME_148_NEXT_RANDOM_BYTE.store(1, Ordering::Relaxed);
    let mut provider = CRAFT_CHROME_148_PROVIDER.clone();
    provider.secure_random = &CHROME_148_INCREMENTING_RANDOM;

    let mut seen_lengths = Vec::new();
    for _ in 0..8 {
        let mut config = ClientConfig::builder(Arc::new(provider.clone()))
            .with_root_certificates(roots())
            .with_no_client_auth()
            .unwrap()
            .with_fingerprint(crate::craft::CHROME_148.builder());
        config.enable_sni = false;

        let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
        let raw = RawClientHello::parse(&wire);
        let (_, aead, _config_id, enc, encrypted_payload) =
            decode_ech_outer_payload(raw.extension(ExtensionType::EncryptedClientHello));

        assert_eq!(aead, HpkeAead::AES_128_GCM.0);
        assert_eq!(enc.len(), 32);
        assert!([144, 176, 208, 240].contains(&encrypted_payload.len()));
        if !seen_lengths.contains(&encrypted_payload.len()) {
            seen_lengths.push(encrypted_payload.len());
        }
    }

    assert!(
        seen_lengths.len() > 1,
        "BoringSSL-style ECH GREASE should vary payload size"
    );
}

#[test]
fn craft_chrome_148_real_ech_inner_matches_boringssl_shape() {
    let config = ClientConfig::builder(Arc::new(tls13_only(CRAFT_CHROME_148_PROVIDER.clone())))
        .with_ech(EchMode::Enable(plaintext_echo_ech_config(true)))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(crate::craft::CHROME_148.builder());

    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let outer = RawClientHello::parse(&wire);
    let (_, _, _, _, encrypted_payload) =
        decode_ech_outer_payload(outer.extension(ExtensionType::EncryptedClientHello));
    let inner = RawClientHelloInner::parse(&encrypted_payload);

    assert_eq!(inner.cipher_suites.len(), 4);
    assert!(is_grease_u16(u16::from(inner.cipher_suites[0])));
    assert_eq!(
        &inner.cipher_suites[1..],
        &[
            CipherSuite::TLS13_AES_128_GCM_SHA256,
            CipherSuite::TLS13_AES_256_GCM_SHA384,
            CipherSuite::TLS13_CHACHA20_POLY1305_SHA256,
        ]
    );
    assert!(
        inner
            .extension_types()
            .contains(&ExtensionType::EncryptedClientHello)
    );
    assert_eq!(
        inner.extension(ExtensionType::EncryptedClientHello),
        &[0x01],
        "ClientHelloInner must keep the ECH inner marker, not a copied outer ECH payload"
    );
    assert_eq!(
        decode_supported_versions(inner.extension(ExtensionType::SupportedVersions)),
        &[ProtocolVersion::TLSv1_3],
        "ClientHelloInner must keep supported_versions while removing TLS 1.2"
    );

    let compressed = decode_u8_prefixed_u16_list(
        inner.extension(ExtensionType::EncryptedClientHelloOuterExtensions),
    )
    .into_iter()
    .map(ExtensionType)
    .collect::<Vec<_>>();
    for ext in [
        ExtensionType::EllipticCurves,
        ExtensionType::SignatureAlgorithms,
        ExtensionType::ALProtocolNegotiation,
        ExtensionType::CompressCertificate,
        ExtensionType::KeyShare,
        ExtensionType::PSKKeyExchangeModes,
    ] {
        assert!(compressed.contains(&ext), "missing compressed {ext:?}");
        assert!(
            !inner.extension_types().contains(&ext),
            "compressed {ext:?} should be represented by ech_outer_extensions"
        );
    }

    let outer_extension_order = outer.extension_types();
    let compressed_positions = compressed
        .iter()
        .map(|ext| {
            outer_extension_order
                .iter()
                .position(|outer_ext| outer_ext == ext)
                .unwrap_or_else(|| panic!("compressed {ext:?} missing from outer hello"))
        })
        .collect::<Vec<_>>();
    assert!(
        compressed_positions
            .windows(2)
            .all(|pair| pair[0] < pair[1]),
        "ech_outer_extensions must follow ClientHelloOuter wire order"
    );
}

#[cfg(all(feature = "brotli", feature = "zlib", feature = "zstd"))]
#[test]
fn craftlsmaxxing_emits_https_safe_maximal_shuffled_surface() {
    let config = ClientConfig::builder(Arc::new(CRAFTLSMAXXING_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(crate::craft::CRAFTLSMAXXING.builder());
    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let raw = RawClientHello::parse(&wire);

    assert!(crate::craft::CRAFTLSMAXXING.shuffle_extensions);
    assert_eq!(raw.cipher_suites.len(), 10);
    assert!(is_grease_u16(u16::from(raw.cipher_suites[0])));
    assert!(
        raw.cipher_suites
            .contains(&CipherSuite::TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256)
    );

    let extension_types = raw.extension_types();
    assert!(is_grease_u16(u16::from(extension_types[0])));
    assert_eq!(*extension_types.last().unwrap(), ExtensionType::Padding);
    for (idx, ext) in extension_types.iter().enumerate() {
        assert!(
            !extension_types[..idx].contains(ext),
            "duplicate extension {ext:?}"
        );
    }

    for ext in [
        ExtensionType::ServerName,
        ExtensionType::ExtendedMasterSecret,
        ExtensionType::RenegotiationInfo,
        ExtensionType::EllipticCurves,
        ExtensionType::ECPointFormats,
        ExtensionType::SessionTicket,
        ExtensionType::StatusRequest,
        ExtensionType::SignatureAlgorithms,
        ExtensionType::SignatureAlgorithmsCert,
        ExtensionType::ALProtocolNegotiation,
        ExtensionType::SCT,
        ExtensionType::DelegatedCredential,
        ExtensionType::KeyShare,
        ExtensionType::PSKKeyExchangeModes,
        ExtensionType::SupportedVersions,
        ExtensionType::CompressCertificate,
        ExtensionType::EncryptedClientHello,
        ExtensionType::RecordSizeLimit,
        ExtensionType::PostHandshakeAuth,
        ExtensionType::CertificateAuthorities,
        ExtensionType::ApplicationSettingsOld,
        ExtensionType::ApplicationSettings,
        ExtensionType::Padding,
    ] {
        assert!(extension_types.contains(&ext), "missing extension {ext:?}");
    }
    for ext in [
        ExtensionType::ClientCertificateType,
        ExtensionType::ServerCertificateType,
        ExtensionType::UseSRTP,
        ExtensionType::NextProtocolNegotiation,
        ExtensionType::ChannelId,
        ExtensionType::TransportParameters,
        ExtensionType::QuicTransportParametersLegacy,
        ExtensionType::TrustAnchors,
        ExtensionType::Pake,
    ] {
        assert!(
            !extension_types.contains(&ext),
            "HTTPS-safe maxxing fingerprint should not emit {ext:?}"
        );
    }

    let protocols = decode_alpn_protocols(raw.extension(ExtensionType::ALProtocolNegotiation));
    assert_eq!(protocols.len(), 3);
    assert_eq!(protocols[0].len(), 2);
    assert!(is_grease_u16(u16::from_be_bytes([
        protocols[0][0],
        protocols[0][1]
    ])));
    assert_eq!(protocols[1], b"h2");
    assert_eq!(protocols[2], b"http/1.1");

    let groups = decode_u16_list(raw.extension(ExtensionType::EllipticCurves))
        .into_iter()
        .map(NamedGroup)
        .collect::<Vec<_>>();
    assert!(is_grease_u16(u16::from(groups[0])));
    for group in [
        NamedGroup::X25519MLKEM768,
        NamedGroup::X25519,
        NamedGroup::secp256r1,
        NamedGroup::secp384r1,
        NamedGroup::secp521r1,
        NamedGroup::FFDHE2048,
        NamedGroup::FFDHE8192,
    ] {
        assert!(groups.contains(&group), "missing group {group:?}");
    }

    let key_shares = decode_key_shares(raw.extension(ExtensionType::KeyShare));
    assert!(is_grease_u16(u16::from(key_shares[0].0)));
    assert!(
        key_shares
            .iter()
            .any(|(group, _)| *group == NamedGroup::X25519MLKEM768)
    );
    assert!(
        key_shares
            .iter()
            .any(|(group, _)| *group == NamedGroup::FFDHE8192)
    );

    let sigalgs = decode_u16_list(raw.extension(ExtensionType::SignatureAlgorithms));
    assert!(is_grease_u16(sigalgs[0]));
    assert!(sigalgs.contains(&u16::from(SignatureScheme::ML_DSA_87)));

    let authorities =
        decode_certificate_authorities(raw.extension(ExtensionType::CertificateAuthorities));
    assert_eq!(authorities.len(), 16);
    for authority in authorities {
        assert_eq!(authority.len(), 32);
        assert!(
            authority.iter().all(|byte| byte.is_ascii_alphanumeric()
                || matches!(*byte, b'\\' | b'.' | b'-')),
            "certificate authority name contains bytes outside the craftlsmaxxing alphabet"
        );
    }

    let (_, _, _, _, ech_payload) =
        decode_ech_outer_payload(raw.extension(ExtensionType::EncryptedClientHello));
    assert!((144..=240).contains(&ech_payload.len()));
}

#[test]
fn craft_can_emit_boringssl_and_nss_extension_surface() {
    let extensions = Box::leak(
        vec![
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::ProtocolsWithGrease(
                Box::leak(
                    vec![
                        crate::craft::GreaseOrProtocol::Protocol(b"http/1.1"),
                        crate::craft::GreaseOrProtocol::Grease,
                    ]
                    .into_boxed_slice(),
                ),
            )),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::SignatureAlgorithms(
                Box::leak(
                    vec![
                        crate::craft::GreaseOr::T(SignatureScheme::ED25519),
                        crate::craft::GreaseOr::Grease,
                    ]
                    .into_boxed_slice(),
                ),
            )),
            crate::craft::ExtensionSpec::Craft(
                crate::craft::CraftExtension::SignatureAlgorithmsCert(Box::leak(
                    vec![
                        crate::craft::GreaseOr::T(SignatureScheme::ECDSA_NISTP256_SHA256),
                        crate::craft::GreaseOr::Grease,
                    ]
                    .into_boxed_slice(),
                )),
            ),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::DelegatedCredentials(
                Box::leak(
                    vec![
                        crate::craft::GreaseOr::T(SignatureScheme::ECDSA_NISTP384_SHA384),
                        crate::craft::GreaseOr::Grease,
                    ]
                    .into_boxed_slice(),
                ),
            )),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::PresharedKeyModes(
                Box::leak(
                    vec![
                        crate::craft::GreaseOrPskKeyExchangeMode::T(
                            crate::craft::PSKKeyExchangeMode::PSK_DHE_KE,
                        ),
                        crate::craft::GreaseOrPskKeyExchangeMode::Grease,
                    ]
                    .into_boxed_slice(),
                ),
            )),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::RecordSizeLimit(
                0x4001,
            )),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::ApplicationSettings {
                codepoint: crate::craft::ApplicationSettingsCodepoint::New,
                protocols: &[b"h2"],
            }),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::ApplicationSettings {
                codepoint: crate::craft::ApplicationSettingsCodepoint::Old,
                protocols: &[b"h2"],
            }),
            crate::craft::ExtensionSpec::Craft(
                crate::craft::CraftExtension::NextProtocolNegotiation,
            ),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::ChannelId),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::UseSrtp {
                profiles: &[0x0001, 0x0002],
                mki: &[0xaa],
            }),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::PostHandshakeAuth),
            crate::craft::ExtensionSpec::Craft(
                crate::craft::CraftExtension::ClientCertificateTypes(&[
                    CertificateType::X509,
                    CertificateType::RawPublicKey,
                ]),
            ),
            crate::craft::ExtensionSpec::Craft(
                crate::craft::CraftExtension::ServerCertificateTypes(&[
                    CertificateType::RawPublicKey,
                ]),
            ),
            crate::craft::ExtensionSpec::Craft(
                crate::craft::CraftExtension::CertificateAuthorities(&[b"ca"]),
            ),
            crate::craft::ExtensionSpec::Craft(
                crate::craft::CraftExtension::QuicTransportParametersLegacy(&[1, 2, 3]),
            ),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::TrustAnchors(&[
                b"ta",
            ])),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::Pake(&[4, 5])),
            crate::craft::ExtensionSpec::Craft(crate::craft::CraftExtension::Raw(
                ExtensionType(0x1234),
                &[9],
            )),
        ]
        .into_boxed_slice(),
    );
    let ciphers = Box::leak(vec![CipherSuite(0xff13).into()].into_boxed_slice());
    let fingerprint = crate::craft::Fingerprint {
        extensions,
        shuffle_extensions: false,
        cipher: ciphers,
        ech_force_tls13: None,
        ech_padding_style: crate::craft::EchPaddingStyle::Standard,
    };
    let config = ClientConfig::builder(Arc::new(CRAFT_CHROME_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(fingerprint.builder());
    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let raw = RawClientHello::parse(&wire);

    let protocols = decode_alpn_protocols(raw.extension(ExtensionType::ALProtocolNegotiation));
    assert_eq!(protocols.len(), 2);
    assert_eq!(protocols[0], b"http/1.1");
    assert_eq!(protocols[1].len(), 2);
    assert!(is_grease_u16(u16::from_be_bytes([
        protocols[1][0],
        protocols[1][1]
    ])));

    let sigalgs = decode_u16_list(raw.extension(ExtensionType::SignatureAlgorithms));
    assert_eq!(sigalgs[0], u16::from(SignatureScheme::ED25519));
    assert!(is_grease_u16(sigalgs[1]));

    let sigalgs_cert = decode_u16_list(raw.extension(ExtensionType::SignatureAlgorithmsCert));
    assert_eq!(
        sigalgs_cert[0],
        u16::from(SignatureScheme::ECDSA_NISTP256_SHA256)
    );
    assert!(is_grease_u16(sigalgs_cert[1]));

    let delegated = decode_u16_list(raw.extension(ExtensionType::DelegatedCredential));
    assert_eq!(
        delegated[0],
        u16::from(SignatureScheme::ECDSA_NISTP384_SHA384)
    );
    assert!(is_grease_u16(delegated[1]));

    let psk_modes = decode_u8_list(raw.extension(ExtensionType::PSKKeyExchangeModes));
    assert_eq!(psk_modes[0], 1);
    assert!(is_grease_psk_key_exchange_mode(psk_modes[1]));

    assert_eq!(raw.extension(ExtensionType::RecordSizeLimit), &[0x40, 0x01]);
    assert_eq!(
        raw.extension(ExtensionType::ApplicationSettings),
        &[0, 3, 2, b'h', b'2']
    );
    assert_eq!(
        raw.extension(ExtensionType::ApplicationSettingsOld),
        &[0, 3, 2, b'h', b'2']
    );
    assert_eq!(raw.extension(ExtensionType::NextProtocolNegotiation), &[]);
    assert_eq!(raw.extension(ExtensionType::ChannelId), &[]);
    assert_eq!(
        raw.extension(ExtensionType::UseSRTP),
        &[0, 4, 0, 1, 0, 2, 1, 0xaa]
    );
    assert_eq!(raw.extension(ExtensionType::PostHandshakeAuth), &[]);
    assert_eq!(
        raw.extension(ExtensionType::ClientCertificateType),
        &[2, 0, 2]
    );
    assert_eq!(raw.extension(ExtensionType::ServerCertificateType), &[1, 2]);
    assert_eq!(
        raw.extension(ExtensionType::CertificateAuthorities),
        &[0, 4, 0, 2, b'c', b'a']
    );
    assert_eq!(
        raw.extension(ExtensionType::QuicTransportParametersLegacy),
        &[1, 2, 3]
    );
    assert_eq!(
        raw.extension(ExtensionType::TrustAnchors),
        &[0, 3, 2, b't', b'a']
    );
    assert_eq!(raw.extension(ExtensionType::Pake), &[4, 5]);
    assert_eq!(raw.extension(ExtensionType(0x1234)), &[9]);
}

#[test]
fn craft_ech_placeholders_change_between_client_hellos() {
    fn assert_placeholder_changes(
        fingerprint: &crate::craft::FingerprintSet,
        payload_body_len: usize,
        random_config_id: bool,
        expected_first_aead: HpkeAead,
        expected_second_aead: HpkeAead,
    ) {
        NEXT_RANDOM_BYTE.store(1, Ordering::Relaxed);
        let mut provider = CRAFT_CHROME_PROVIDER.clone();
        provider.secure_random = &INCREMENTING_RANDOM;

        let make_config = || {
            ClientConfig::builder(Arc::new(provider.clone()))
                .with_root_certificates(roots())
                .with_no_client_auth()
                .unwrap()
                .with_fingerprint(
                    fingerprint
                        .builder()
                        .do_not_override_certificate_compression()
                        .dangerous_disable_override_supported_curves()
                        .dangerous_disable_override_keyshare(),
                )
        };

        let (_, first_wire) = client_hello_sent_for_config_with_wire(make_config()).unwrap();
        let (_, second_wire) = client_hello_sent_for_config_with_wire(make_config()).unwrap();
        let first = RawClientHello::parse(&first_wire);
        let second = RawClientHello::parse(&second_wire);

        let first_ech = first.extension(ExtensionType::EncryptedClientHello);
        let second_ech = second.extension(ExtensionType::EncryptedClientHello);

        assert_eq!(first_ech.len(), 42 + payload_body_len);
        assert_eq!(second_ech.len(), 42 + payload_body_len);
        assert_eq!(&first_ech[..3], &[0, 0, 1]);
        assert_eq!(&second_ech[..3], &[0, 0, 1]);
        assert_eq!(&first_ech[3..5], &expected_first_aead.0.to_be_bytes());
        assert_eq!(&second_ech[3..5], &expected_second_aead.0.to_be_bytes());
        if random_config_id {
            assert_ne!(first_ech[5], second_ech[5]);
        } else {
            assert_eq!(first_ech[5], 0);
            assert_eq!(second_ech[5], 0);
        }
        assert_eq!(&first_ech[6..8], &[0, 0x20]);
        assert_eq!(&second_ech[6..8], &[0, 0x20]);
        assert_eq!(&first_ech[40..42], &(payload_body_len as u16).to_be_bytes());
        assert_eq!(
            &second_ech[40..42],
            &(payload_body_len as u16).to_be_bytes()
        );
        assert_ne!(&first_ech[8..40], &second_ech[8..40]);
        assert_ne!(&first_ech[42..], &second_ech[42..]);
    }

    assert_placeholder_changes(
        &crate::craft::CHROMIUM_144,
        1366,
        false,
        HpkeAead::AES_128_GCM,
        HpkeAead::AES_128_GCM,
    );
    assert_placeholder_changes(
        &crate::craft::FIREFOX_140,
        239,
        true,
        HpkeAead::AES_128_GCM,
        HpkeAead::CHACHA20_POLY_1305,
    );
}

#[test]
fn craft_ech_placeholder_marks_connection_as_grease_ech() {
    let config = ClientConfig::builder(Arc::new(CRAFT_CHROME_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(
            crate::craft::FIREFOX_140
                .builder()
                .do_not_override_certificate_compression()
                .dangerous_disable_override_supported_curves()
                .dangerous_disable_override_keyshare(),
        );

    let mut conn = Arc::new(config)
        .connect(ServerName::try_from("localhost").unwrap())
        .build()
        .unwrap();
    let mut bytes = Vec::new();
    conn.write_tls(&mut bytes).unwrap();

    assert_eq!(conn.ech_status(), EchStatus::Grease);
}

#[test]
fn craft_real_ech_replaces_placeholder_before_encrypting_outer() {
    use crate::craft::CraftExtension::*;
    use crate::craft::ExtensionSpec::*;
    use crate::craft::GreaseOr::T;
    use crate::craft::KeepExtension::*;

    let extensions = Box::leak(
        vec![
            Keep(Must(ExtensionType::ServerName)),
            Craft(Protocols(&[b"h2", b"http/1.1"])),
            Craft(EchPlaceholder {
                payload_body_len: 8,
                random_config_id: false,
                aead: crate::craft::EchPlaceholderAead::Fixed(HpkeAead::AES_128_GCM),
            }),
            Craft(SupportedVersions(Box::leak(
                vec![T(ProtocolVersion::TLSv1_3)].into_boxed_slice(),
            ))),
        ]
        .into_boxed_slice(),
    );
    let ciphers = Box::leak(vec![CipherSuite::TLS13_AES_128_GCM_SHA256.into()].into_boxed_slice());
    let fingerprint = crate::craft::Fingerprint {
        extensions,
        shuffle_extensions: false,
        cipher: ciphers,
        ech_force_tls13: None,
        ech_padding_style: crate::craft::EchPaddingStyle::Standard,
    };
    let ech_config = EchConfig {
        config: EchConfigPayload::V18(EchConfigContents {
            key_config: HpkeKeyConfig {
                config_id: 7,
                kem_id: AadEchoHpke::SUITE.kem,
                public_key: vec![0; 32].into(),
                symmetric_cipher_suites: vec![AadEchoHpke::SUITE.sym],
            },
            maximum_name_length: 64,
            public_name: DnsName::try_from("public.example").unwrap(),
            extensions: vec![],
        }),
        suite: &AAD_ECHO_HPKE,
        tls13_only: true,
    };
    let config = ClientConfig::builder(Arc::new(tls13_only(CRAFT_CHROME_PROVIDER.clone())))
        .with_ech(EchMode::Enable(ech_config))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(fingerprint.builder());

    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let raw = RawClientHello::parse(&wire);
    let final_types = raw
        .extensions
        .iter()
        .map(|extension| extension.typ)
        .collect::<Vec<_>>();
    assert_eq!(
        final_types,
        &[
            ExtensionType::ServerName,
            ExtensionType::ALProtocolNegotiation,
            ExtensionType::EncryptedClientHello,
            ExtensionType::SupportedVersions,
        ]
    );

    let final_ech = raw.extension(ExtensionType::EncryptedClientHello);
    let (_, final_aead, final_config_id, final_enc, encrypted_payload) =
        decode_ech_outer_payload(final_ech);
    assert_eq!(final_aead, HpkeAead::AES_128_GCM.0);
    assert_eq!(final_config_id, 7);
    assert_eq!(final_enc, vec![0xee; 4]);

    // The test HPKE sealer returns the AAD as ciphertext, so we can verify the
    // encrypted ECH payload was computed over the crafted outer ClientHello.
    let aad_raw = RawClientHello::parse(&wrap_client_hello_body(&encrypted_payload));
    let aad_types = aad_raw
        .extensions
        .iter()
        .map(|extension| extension.typ)
        .collect::<Vec<_>>();
    assert_eq!(aad_types, final_types);
    assert_eq!(
        aad_raw.extension(ExtensionType::ALProtocolNegotiation),
        raw.extension(ExtensionType::ALProtocolNegotiation)
    );

    let aad_ech = aad_raw.extension(ExtensionType::EncryptedClientHello);
    let (_, aad_aead, aad_config_id, aad_enc, aad_placeholder) = decode_ech_outer_payload(aad_ech);
    assert_eq!(aad_aead, HpkeAead::AES_128_GCM.0);
    assert_eq!(aad_config_id, 7);
    assert_eq!(aad_enc, vec![0xee; 4]);
    assert!(
        aad_placeholder
            .iter()
            .all(|byte| *byte == 0)
    );
}

#[test]
fn craft_real_ech_compresses_crafted_outer_extensions() {
    use crate::craft::CraftExtension::*;
    use crate::craft::ExtensionSpec::*;
    use crate::craft::GreaseOr::T;
    use crate::craft::KeepExtension::*;

    let extensions = Box::leak(
        vec![
            Keep(Must(ExtensionType::ServerName)),
            Craft(KeyShare(Box::leak(
                vec![T(NamedGroup::secp256r1)].into_boxed_slice(),
            ))),
            Craft(SupportedVersions(Box::leak(
                vec![T(ProtocolVersion::TLSv1_3)].into_boxed_slice(),
            ))),
        ]
        .into_boxed_slice(),
    );
    let ciphers = Box::leak(vec![CipherSuite::TLS13_AES_128_GCM_SHA256.into()].into_boxed_slice());
    let fingerprint = crate::craft::Fingerprint {
        extensions,
        shuffle_extensions: false,
        cipher: ciphers,
        ech_force_tls13: None,
        ech_padding_style: crate::craft::EchPaddingStyle::Nss,
    };
    let config = ClientConfig::builder(Arc::new(tls13_only(CRAFT_CHROME_PROVIDER.clone())))
        .with_ech(EchMode::Enable(plaintext_echo_ech_config(true)))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(fingerprint.builder());

    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let outer = RawClientHello::parse(&wire);
    let outer_key_shares = decode_key_shares(outer.extension(ExtensionType::KeyShare));
    assert_eq!(outer_key_shares[0].0, NamedGroup::secp256r1);

    let (_, _, _, _, encrypted_payload) =
        decode_ech_outer_payload(outer.extension(ExtensionType::EncryptedClientHello));
    let inner = RawClientHelloInner::parse(&encrypted_payload);
    assert!(
        !inner
            .extension_types()
            .contains(&ExtensionType::KeyShare)
    );
    let outer_extensions = decode_u8_prefixed_u16_list(
        inner.extension(ExtensionType::EncryptedClientHelloOuterExtensions),
    )
    .into_iter()
    .map(ExtensionType)
    .collect::<Vec<_>>();
    assert_eq!(
        outer_extensions,
        &[ExtensionType::KeyShare, ExtensionType::SupportedVersions]
    );
}

#[test]
fn ech_tls13_only_default_rejects_tls12_capable_provider() {
    let err = ClientConfig::builder(Arc::new(CRAFT_CHROME_PROVIDER.clone()))
        .with_ech(EchMode::Enable(aad_echo_ech_config(true)))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap_err();

    assert!(matches!(
        err,
        Error::ApiMisuse(ApiMisuse::EchForbidsTls12Support)
    ));
}

#[test]
fn ech_tls13_only_can_be_disabled_with_tls12_capable_provider() {
    let config = ClientConfig::builder(Arc::new(CRAFT_CHROME_PROVIDER.clone()))
        .with_ech(EchMode::Enable(aad_echo_ech_config(false)))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap();

    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let raw = RawClientHello::parse(&wire);

    assert!(
        raw.cipher_suites
            .contains(&CipherSuite::TLS_EMPTY_RENEGOTIATION_INFO_SCSV)
    );
    assert_eq!(
        decode_supported_versions(raw.extension(ExtensionType::SupportedVersions)),
        &[ProtocolVersion::TLSv1_3, ProtocolVersion::TLSv1_2]
    );
}

#[test]
fn craft_firefox_140_disables_ech_tls13_only() {
    assert_eq!(
        crate::craft::FIREFOX_140
            .builder()
            .ech_force_tls13(),
        Some(false)
    );
}

#[cfg(all(feature = "zlib", feature = "brotli", feature = "zstd"))]
#[test]
fn craft_firefox_140_advertises_requested_certificate_compression_when_supported() {
    let config = ClientConfig::builder(Arc::new(CRAFT_CHROME_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(
            crate::craft::FIREFOX_140
                .builder()
                .dangerous_disable_override_supported_curves()
                .dangerous_disable_override_keyshare(),
        );
    let (_, wire) = client_hello_sent_for_config_with_wire(config).unwrap();
    let raw = RawClientHello::parse(&wire);

    let payload = raw.extension(ExtensionType::CompressCertificate);
    let mut offset = 0;
    let len = take_u8(payload, &mut offset) as usize;
    assert_eq!(payload.len(), offset + len);
    assert_eq!(len % 2, 0);
    let advertised = take(payload, &mut offset, len)
        .chunks_exact(2)
        .map(|chunk| CertificateCompressionAlgorithm(u16::from_be_bytes([chunk[0], chunk[1]])))
        .collect::<Vec<_>>();

    let expected = [
        CertificateCompressionAlgorithm::Zlib,
        CertificateCompressionAlgorithm::Brotli,
        CertificateCompressionAlgorithm::Zstd,
    ];

    assert_eq!(advertised, expected);
}

#[test]
fn craft_fingerprint_errors_instead_of_changing_unsupported_compression() {
    let mut config = ClientConfig::builder(Arc::new(CRAFT_CHROME_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(
            crate::craft::FIREFOX_140
                .builder()
                .dangerous_disable_override_supported_curves()
                .dangerous_disable_override_keyshare(),
        );
    config
        .cert_decompressors
        .retain(|decompressor| decompressor.algorithm() != CertificateCompressionAlgorithm::Zstd);

    let err = match client_hello_sent_for_config_with_wire(config).unwrap_err() {
        Error::General(err) => err,
        err => panic!("unexpected error {err:?}"),
    };
    assert!(err.contains("craft fingerprint requires unsupported certificate compression"));
    assert!(err.contains("Zstd"));
}

#[test]
fn craft_fingerprint_errors_instead_of_faking_unsupported_curve() {
    let extensions = Box::leak(
        vec![crate::craft::ExtensionSpec::Craft(
            crate::craft::CraftExtension::SupportedCurves(Box::leak(
                vec![crate::craft::GreaseOr::T(NamedGroup::secp521r1)].into_boxed_slice(),
            )),
        )]
        .into_boxed_slice(),
    );
    let ciphers = Box::leak(vec![CipherSuite(0xff13).into()].into_boxed_slice());
    let fingerprint = crate::craft::Fingerprint {
        extensions,
        shuffle_extensions: false,
        cipher: ciphers,
        ech_force_tls13: None,
        ech_padding_style: crate::craft::EchPaddingStyle::Standard,
    };
    let config = ClientConfig::builder(Arc::new(TEST_PROVIDER.clone()))
        .with_root_certificates(roots())
        .with_no_client_auth()
        .unwrap()
        .with_fingerprint(fingerprint.builder());

    let err = match client_hello_sent_for_config_with_wire(config).unwrap_err() {
        Error::General(err) => err,
        err => panic!("unexpected error {err:?}"),
    };
    assert!(err.contains("craft fingerprint requires unsupported named group"));
    assert!(err.contains("secp521r1"));
}

#[test]
fn craft_fingerprints_complete_handshakes_with_varied_server_configs() {
    let fingerprint = craft_boringssl_nss_interop_fingerprint();

    assert_craft_handshake(
        craft_client_config(TEST_PROVIDER.clone(), fingerprint.builder(), None),
        craft_server_config(tls13_only(TEST_PROVIDER.clone()), &[b"http/1.1"]),
        ProtocolVersion::TLSv1_3,
        Some(b"http/1.1"),
        2,
    );

    assert_craft_handshake(
        craft_client_config(TEST_PROVIDER.clone(), fingerprint.builder(), None),
        craft_server_config(tls12_only(TEST_PROVIDER.clone()), &[b"http/1.1"]),
        ProtocolVersion::TLSv1_2,
        Some(b"http/1.1"),
        1,
    );

    assert_craft_handshake(
        craft_client_config(TEST_PROVIDER.clone(), fingerprint.builder(), None),
        craft_server_config(tls13_only(TEST_PROVIDER.clone()), &[]),
        ProtocolVersion::TLSv1_3,
        None,
        1,
    );
}

#[test]
fn craft_ffdhe_supported_group_can_complete_tls13_hrr() {
    let ffdhe_group =
        crate::craft::to_missing_kx_group(&NamedGroup::FFDHE2048, TEST_PROVIDER.secure_random)
            .unwrap();
    let server_provider = CryptoProvider {
        kx_groups: Cow::Owned(vec![ffdhe_group]),
        ..TEST_PROVIDER
    };

    assert_craft_handshake(
        craft_client_config(
            CRAFT_CHROME_PROVIDER.clone(),
            craft_ffdhe_hrr_fingerprint().builder(),
            Some(&[b"http/1.1"]),
        ),
        craft_server_config(tls13_only(server_provider), &[b"http/1.1"]),
        ProtocolVersion::TLSv1_3,
        Some(b"http/1.1"),
        1,
    );
}

#[test]
fn craft_can_preserve_rustls_generated_options_for_server_compatibility() {
    let fingerprint = craft_incompatible_override_fingerprint();
    let mut builder = fingerprint
        .builder()
        .do_not_override_alpn()
        .do_not_override_versions()
        .dangerous_disable_override_supported_curves()
        .dangerous_disable_override_keyshare();

    assert_craft_handshake(
        craft_client_config(TEST_PROVIDER.clone(), builder, Some(&[b"http/1.1"])),
        craft_server_config(tls12_only(TEST_PROVIDER.clone()), &[b"http/1.1"]),
        ProtocolVersion::TLSv1_2,
        Some(b"http/1.1"),
        1,
    );

    builder = fingerprint
        .builder()
        .do_not_override_alpn()
        .do_not_override_versions()
        .dangerous_disable_override_supported_curves()
        .dangerous_disable_override_keyshare();

    assert_craft_handshake(
        craft_client_config(TEST_PROVIDER.clone(), builder, Some(&[b"http/1.1"])),
        craft_server_config(tls13_only(TEST_PROVIDER.clone()), &[b"http/1.1"]),
        ProtocolVersion::TLSv1_3,
        Some(b"http/1.1"),
        1,
    );
}

fn craft_client_config(
    provider: CryptoProvider,
    fingerprint: crate::craft::FingerprintBuilder,
    alpn_protocols: Option<&[&[u8]]>,
) -> Arc<ClientConfig> {
    let mut config = ClientConfig::builder(Arc::new(provider))
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(AcceptsAnyServer))
        .with_no_client_auth()
        .unwrap();

    if let Some(alpn_protocols) = alpn_protocols {
        config.alpn_protocols = alpn_protocols
            .iter()
            .map(|protocol| ApplicationProtocol::from(*protocol).to_owned())
            .collect();
    }

    Arc::new(config.with_fingerprint(fingerprint))
}

fn craft_server_config(provider: CryptoProvider, alpn_protocols: &[&[u8]]) -> Arc<ServerConfig> {
    let mut config = ServerConfig::builder(Arc::new(provider))
        .with_no_client_auth()
        .with_single_cert(craft_server_identity(), craft_server_key())
        .unwrap();
    config.session_storage = Arc::new(NoServerSessionStorage {});
    config.alpn_protocols = alpn_protocols
        .iter()
        .map(|protocol| ApplicationProtocol::from(*protocol).to_owned())
        .collect();
    Arc::new(config)
}

fn assert_craft_handshake(
    client_config: Arc<ClientConfig>,
    server_config: Arc<ServerConfig>,
    expected_version: ProtocolVersion,
    expected_alpn: Option<&[u8]>,
    handshakes: usize,
) {
    let server_name = ServerName::try_from("localhost")
        .unwrap()
        .to_owned();

    for _ in 0..handshakes {
        let mut client = client_config
            .connect(server_name.clone())
            .build()
            .unwrap();
        let mut server = ServerConnection::new(server_config.clone()).unwrap();

        drive_craft_pair(&mut client, &mut server);
        flush_craft_pair(&mut client, &mut server);

        assert_eq!(client.protocol_version(), Some(expected_version));
        assert_eq!(server.protocol_version(), Some(expected_version));
        assert_eq!(
            client
                .alpn_protocol()
                .map(|protocol| protocol.as_ref()),
            expected_alpn
        );
        assert_eq!(
            server
                .alpn_protocol()
                .map(|protocol| protocol.as_ref()),
            expected_alpn
        );

        client
            .writer()
            .write_all(b"ping")
            .unwrap();
        assert!(transfer_craft_tls(&mut client, &mut server));

        let mut received = [0; 4];
        server
            .reader()
            .read_exact(&mut received)
            .unwrap();
        assert_eq!(&received, b"ping");

        server
            .writer()
            .write_all(b"pong")
            .unwrap();
        assert!(transfer_craft_tls(&mut server, &mut client));

        let mut received = [0; 4];
        client
            .reader()
            .read_exact(&mut received)
            .unwrap();
        assert_eq!(&received, b"pong");
    }
}

fn drive_craft_pair(client: &mut dyn Connection, server: &mut dyn Connection) {
    for _ in 0..32 {
        let progressed = transfer_craft_tls(client, server) | transfer_craft_tls(server, client);
        if !client.is_handshaking() && !server.is_handshaking() {
            return;
        }
        assert!(progressed, "craft handshake stalled");
    }

    panic!("craft handshake did not complete");
}

fn flush_craft_pair(left: &mut dyn Connection, right: &mut dyn Connection) {
    for _ in 0..8 {
        if !(transfer_craft_tls(left, right) | transfer_craft_tls(right, left)) {
            return;
        }
    }
}

fn transfer_craft_tls(from: &mut dyn Connection, to: &mut dyn Connection) -> bool {
    let mut bytes = Vec::new();
    let written = from.write_tls(&mut bytes).unwrap();
    if written == 0 {
        return false;
    }

    let read = to
        .read_tls(&mut bytes.as_slice())
        .unwrap();
    assert_eq!(read, bytes.len());
    to.process_new_packets().unwrap();
    true
}

fn craft_boringssl_nss_interop_fingerprint() -> crate::craft::Fingerprint {
    use crate::craft::CraftExtension::*;
    use crate::craft::ExtensionSpec::*;
    use crate::craft::GreaseOr::{Grease, T};
    use crate::craft::GreaseOrProtocol;
    use crate::craft::GreaseOrPskKeyExchangeMode;
    use crate::craft::KeepExtension::*;

    let extensions = Box::leak(
        vec![
            Craft(Grease1),
            Keep(Must(ExtensionType::ServerName)),
            Rustls(crate::craft::ClientExtension::ExtendedMasterSecretRequest),
            Craft(RenegotiationInfo),
            Craft(SupportedCurves(Box::leak(
                vec![Grease, T(NamedGroup(0xfe00))].into_boxed_slice(),
            ))),
            Rustls(crate::craft::ClientExtension::EcPointFormats(vec![
                crate::craft::ECPointFormat::Uncompressed,
            ])),
            Keep(Optional(ExtensionType::SessionTicket)),
            Craft(ProtocolsWithGrease(Box::leak(
                vec![
                    GreaseOrProtocol::Protocol(b"http/1.1"),
                    GreaseOrProtocol::Grease,
                ]
                .into_boxed_slice(),
            ))),
            Craft(SignatureAlgorithms(Box::leak(
                vec![T(SignatureScheme::ECDSA_NISTP256_SHA256), Grease].into_boxed_slice(),
            ))),
            Craft(SignatureAlgorithmsCert(Box::leak(
                vec![T(SignatureScheme::ECDSA_NISTP256_SHA256), Grease].into_boxed_slice(),
            ))),
            Craft(SignedCertificateTimestamp),
            Craft(DelegatedCredentials(Box::leak(
                vec![T(SignatureScheme::ECDSA_NISTP256_SHA256), Grease].into_boxed_slice(),
            ))),
            Craft(KeyShare(Box::leak(
                vec![Grease, T(NamedGroup(0xfe00))].into_boxed_slice(),
            ))),
            Craft(PresharedKeyModes(Box::leak(
                vec![
                    GreaseOrPskKeyExchangeMode::T(crate::craft::PSKKeyExchangeMode::PSK_DHE_KE),
                    GreaseOrPskKeyExchangeMode::Grease,
                ]
                .into_boxed_slice(),
            ))),
            Keep(Optional(ExtensionType::EarlyData)),
            Craft(SupportedVersions(Box::leak(
                vec![
                    Grease,
                    T(ProtocolVersion::TLSv1_3),
                    T(ProtocolVersion::TLSv1_2),
                ]
                .into_boxed_slice(),
            ))),
            Craft(RecordSizeLimit(0x4001)),
            Craft(ApplicationSettings {
                codepoint: crate::craft::ApplicationSettingsCodepoint::New,
                protocols: &[b"h2"],
            }),
            Craft(ApplicationSettings {
                codepoint: crate::craft::ApplicationSettingsCodepoint::Old,
                protocols: &[b"h2"],
            }),
            Craft(NextProtocolNegotiation),
            Craft(ChannelId),
            Craft(UseSrtp {
                profiles: &[0x0001],
                mki: &[],
            }),
            Craft(PostHandshakeAuth),
            Craft(ClientCertificateTypes(&[CertificateType::X509])),
            Craft(ServerCertificateTypes(&[CertificateType::X509])),
            Craft(CertificateAuthorities(&[b"ca"])),
            Craft(QuicTransportParametersLegacy(&[1, 2, 3])),
            Craft(TrustAnchors(&[b"ta"])),
            Craft(Pake(&[4, 5])),
            Craft(Raw(ExtensionType(0x1234), &[9])),
            Craft(Grease2),
            Craft(Padding),
            Keep(Optional(ExtensionType::PreSharedKey)),
        ]
        .into_boxed_slice(),
    );

    crate::craft::Fingerprint {
        extensions,
        cipher: craft_interop_ciphers(),
        shuffle_extensions: false,
        ech_force_tls13: None,
        ech_padding_style: crate::craft::EchPaddingStyle::Standard,
    }
}

fn craft_incompatible_override_fingerprint() -> crate::craft::Fingerprint {
    use crate::craft::CraftExtension::*;
    use crate::craft::ExtensionSpec::*;
    use crate::craft::GreaseOr::T;
    use crate::craft::KeepExtension::*;

    let extensions = Box::leak(
        vec![
            Keep(Must(ExtensionType::ServerName)),
            Rustls(crate::craft::ClientExtension::ExtendedMasterSecretRequest),
            Craft(SupportedCurves(Box::leak(
                vec![T(NamedGroup(0xfe01))].into_boxed_slice(),
            ))),
            Craft(Protocols(&[b"h2"])),
            Craft(SignatureAlgorithms(Box::leak(
                vec![T(SignatureScheme::ECDSA_NISTP256_SHA256)].into_boxed_slice(),
            ))),
            Craft(KeyShare(Box::leak(
                vec![T(NamedGroup(0xfe01))].into_boxed_slice(),
            ))),
            Rustls(crate::craft::ClientExtension::PresharedKeyModes(vec![
                crate::craft::PSKKeyExchangeMode::PSK_DHE_KE,
            ])),
            Craft(SupportedVersions(Box::leak(
                vec![T(ProtocolVersion::TLSv1_3)].into_boxed_slice(),
            ))),
            Craft(Padding),
        ]
        .into_boxed_slice(),
    );

    crate::craft::Fingerprint {
        extensions,
        cipher: craft_interop_ciphers(),
        shuffle_extensions: false,
        ech_force_tls13: None,
        ech_padding_style: crate::craft::EchPaddingStyle::Standard,
    }
}

fn craft_ffdhe_hrr_fingerprint() -> crate::craft::Fingerprint {
    use crate::craft::CraftExtension::*;
    use crate::craft::ExtensionSpec::*;
    use crate::craft::GreaseOr::T;
    use crate::craft::KeepExtension::*;

    let extensions = Box::leak(
        vec![
            Keep(Must(ExtensionType::ServerName)),
            Craft(SupportedCurves(Box::leak(
                vec![T(NamedGroup::X25519), T(NamedGroup::FFDHE2048)].into_boxed_slice(),
            ))),
            Craft(Protocols(&[b"http/1.1"])),
            Craft(SignatureAlgorithms(Box::leak(
                vec![T(SignatureScheme::ECDSA_NISTP256_SHA256)].into_boxed_slice(),
            ))),
            Craft(KeyShare(Box::leak(
                vec![T(NamedGroup::X25519)].into_boxed_slice(),
            ))),
            Rustls(crate::craft::ClientExtension::PresharedKeyModes(vec![
                crate::craft::PSKKeyExchangeMode::PSK_DHE_KE,
            ])),
            Craft(SupportedVersions(Box::leak(
                vec![T(ProtocolVersion::TLSv1_3)].into_boxed_slice(),
            ))),
        ]
        .into_boxed_slice(),
    );
    let ciphers = Box::leak(vec![CipherSuite(0xff13).into()].into_boxed_slice());

    crate::craft::Fingerprint {
        extensions,
        cipher: ciphers,
        shuffle_extensions: false,
        ech_force_tls13: None,
        ech_padding_style: crate::craft::EchPaddingStyle::Standard,
    }
}

fn craft_interop_ciphers() -> &'static [crate::craft::GreaseOrCipher] {
    Box::leak(
        vec![
            crate::craft::GreaseOrCipher::Grease,
            CipherSuite(0xff13).into(),
            CipherSuite(0xff12).into(),
        ]
        .into_boxed_slice(),
    )
}

fn craft_server_key() -> PrivateKeyDer<'static> {
    PrivateKeyDer::from_pem_reader(
        &mut include_bytes!("../../../test-ca/ecdsa-p256/end.key").as_slice(),
    )
    .unwrap()
}

fn craft_server_identity() -> Arc<Identity<'static>> {
    Arc::new(
        Identity::from_cert_chain(vec![
            CertificateDer::from(&include_bytes!("../../../test-ca/ecdsa-p256/end.der")[..]),
            CertificateDer::from(&include_bytes!("../../../test-ca/ecdsa-p256/inter.der")[..]),
        ])
        .unwrap(),
    )
}

#[derive(Debug)]
struct AcceptsAnyServer;

impl ServerVerifier for AcceptsAnyServer {
    fn verify_identity(&self, _identity: &ServerIdentity<'_>) -> Result<PeerVerified, Error> {
        Ok(PeerVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        _input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn verify_tls13_signature(
        &self,
        _input: &SignatureVerificationInput<'_>,
    ) -> Result<HandshakeSignatureValid, Error> {
        Ok(HandshakeSignatureValid::assertion())
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        vec![
            SignatureScheme::ECDSA_NISTP256_SHA256,
            SignatureScheme::RSA_PSS_SHA256,
            SignatureScheme::RSA_PKCS1_SHA256,
        ]
    }

    fn request_ocsp_response(&self) -> bool {
        false
    }

    fn hash_config(&self, _: &mut dyn Hasher) {}
}

fn client_hello_sent_for_config(config: ClientConfig) -> Result<ClientHelloPayload, Error> {
    client_hello_sent_for_config_with_wire(config).map(|(hello, _)| hello)
}

fn client_hello_sent_for_config_with_wire(
    config: ClientConfig,
) -> Result<(ClientHelloPayload, Vec<u8>), Error> {
    let mut conn = Arc::new(config)
        .connect(ServerName::try_from("localhost").unwrap())
        .build()?;
    let mut bytes = Vec::new();
    conn.write_tls(&mut bytes).unwrap();

    let message = EncodedMessage::<Payload<'_>>::read(&mut Reader::new(&bytes))
        .unwrap()
        .into_owned();
    assert_eq!(message.typ, ContentType::Handshake);
    let wire = message.payload.bytes().to_vec();
    match Message::try_from(&message).unwrap() {
        Message {
            payload:
                MessagePayload::Handshake {
                    parsed: HandshakeMessagePayload(HandshakePayload::ClientHello(ch)),
                    ..
                },
            ..
        } => Ok((ch, wire)),
        other => panic!("unexpected message {other:?}"),
    }
}

#[derive(Debug)]
struct RawClientHello {
    cipher_suites: Vec<CipherSuite>,
    extensions: Vec<RawClientHelloExtension>,
}

impl RawClientHello {
    fn parse(wire: &[u8]) -> Self {
        let mut offset = 0;
        assert_eq!(
            take_u8(wire, &mut offset),
            u8::from(HandshakeType::ClientHello)
        );
        let body_len = take_u24(wire, &mut offset);
        assert_eq!(wire.len(), offset + body_len);

        take(wire, &mut offset, 2); // legacy_version
        take(wire, &mut offset, 32); // random
        let session_id_len = take_u8(wire, &mut offset) as usize;
        take(wire, &mut offset, session_id_len);

        let cipher_suites_len = take_u16(wire, &mut offset) as usize;
        assert_eq!(cipher_suites_len % 2, 0);
        let cipher_suites = take(wire, &mut offset, cipher_suites_len)
            .chunks_exact(2)
            .map(|chunk| CipherSuite(u16::from_be_bytes([chunk[0], chunk[1]])))
            .collect::<Vec<_>>();

        let compression_methods_len = take_u8(wire, &mut offset) as usize;
        take(wire, &mut offset, compression_methods_len);

        let extensions_len = take_u16(wire, &mut offset) as usize;
        let extensions_end = offset + extensions_len;
        assert_eq!(wire.len(), extensions_end);

        let mut extensions = Vec::new();
        while offset < extensions_end {
            let typ = ExtensionType(take_u16(wire, &mut offset));
            let len = take_u16(wire, &mut offset) as usize;
            let payload = take(wire, &mut offset, len).to_vec();
            extensions.push(RawClientHelloExtension { typ, payload });
        }

        Self {
            cipher_suites,
            extensions,
        }
    }

    fn extension(&self, typ: ExtensionType) -> &[u8] {
        self.extensions
            .iter()
            .find(|extension| extension.typ == typ)
            .unwrap_or_else(|| panic!("missing extension {typ:?}"))
            .payload
            .as_slice()
    }

    fn extension_types(&self) -> Vec<ExtensionType> {
        self.extensions
            .iter()
            .map(|extension| extension.typ)
            .collect()
    }
}

#[derive(Debug)]
struct RawClientHelloInner {
    cipher_suites: Vec<CipherSuite>,
    extensions: Vec<RawClientHelloExtension>,
}

impl RawClientHelloInner {
    fn parse(wire: &[u8]) -> Self {
        let mut offset = 0;
        take(wire, &mut offset, 2); // legacy_version
        take(wire, &mut offset, 32); // random
        let session_id_len = take_u8(wire, &mut offset) as usize;
        take(wire, &mut offset, session_id_len);

        let cipher_suites_len = take_u16(wire, &mut offset) as usize;
        assert_eq!(cipher_suites_len % 2, 0);
        let cipher_suites = take(wire, &mut offset, cipher_suites_len)
            .chunks_exact(2)
            .map(|chunk| CipherSuite(u16::from_be_bytes([chunk[0], chunk[1]])))
            .collect::<Vec<_>>();

        let compression_methods_len = take_u8(wire, &mut offset) as usize;
        take(wire, &mut offset, compression_methods_len);

        let extensions_len = take_u16(wire, &mut offset) as usize;
        let extensions_end = offset + extensions_len;
        assert!(extensions_end <= wire.len());

        let mut extensions = Vec::new();
        while offset < extensions_end {
            let typ = ExtensionType(take_u16(wire, &mut offset));
            let len = take_u16(wire, &mut offset) as usize;
            let payload = take(wire, &mut offset, len).to_vec();
            extensions.push(RawClientHelloExtension { typ, payload });
        }

        assert!(
            wire[offset..]
                .iter()
                .all(|byte| *byte == 0)
        );

        Self {
            cipher_suites,
            extensions,
        }
    }

    fn extension(&self, typ: ExtensionType) -> &[u8] {
        self.extensions
            .iter()
            .find(|extension| extension.typ == typ)
            .unwrap_or_else(|| panic!("missing extension {typ:?}"))
            .payload
            .as_slice()
    }

    fn extension_types(&self) -> Vec<ExtensionType> {
        self.extensions
            .iter()
            .map(|extension| extension.typ)
            .collect()
    }
}

#[derive(Debug)]
struct RawClientHelloExtension {
    typ: ExtensionType,
    payload: Vec<u8>,
}

fn wrap_client_hello_body(body: &[u8]) -> Vec<u8> {
    assert!(body.len() <= 0xff_ffff);
    let mut wire = Vec::with_capacity(4 + body.len());
    wire.push(u8::from(HandshakeType::ClientHello));
    wire.push((body.len() >> 16) as u8);
    wire.push((body.len() >> 8) as u8);
    wire.push(body.len() as u8);
    wire.extend_from_slice(body);
    wire
}

fn decode_ech_outer_payload(payload: &[u8]) -> (u16, u16, u8, Vec<u8>, Vec<u8>) {
    let mut offset = 0;
    assert_eq!(take_u8(payload, &mut offset), 0);
    let kdf = take_u16(payload, &mut offset);
    let aead = take_u16(payload, &mut offset);
    let config_id = take_u8(payload, &mut offset);
    let enc_len = take_u16(payload, &mut offset) as usize;
    let enc = take(payload, &mut offset, enc_len).to_vec();
    let payload_len = take_u16(payload, &mut offset) as usize;
    let encrypted_payload = take(payload, &mut offset, payload_len).to_vec();
    assert_eq!(offset, payload.len());
    (kdf, aead, config_id, enc, encrypted_payload)
}

fn decode_supported_versions(payload: &[u8]) -> Vec<ProtocolVersion> {
    let mut offset = 0;
    let len = take_u8(payload, &mut offset) as usize;
    assert_eq!(len % 2, 0);
    let versions = take(payload, &mut offset, len)
        .chunks_exact(2)
        .map(|chunk| ProtocolVersion(u16::from_be_bytes([chunk[0], chunk[1]])))
        .collect::<Vec<_>>();
    assert_eq!(offset, payload.len());
    versions
}

#[derive(Debug)]
struct AadEchoHpke;

static AAD_ECHO_HPKE: AadEchoHpke = AadEchoHpke;

impl AadEchoHpke {
    const SUITE: HpkeSuite = HpkeSuite {
        kem: HpkeKem::DHKEM_P256_HKDF_SHA256,
        sym: HpkeSymmetricCipherSuite {
            kdf_id: HpkeKdf::HKDF_SHA256,
            aead_id: HpkeAead::AES_128_GCM,
        },
    };
}

fn aad_echo_ech_config(tls13_only: bool) -> EchConfig {
    EchConfig {
        config: EchConfigPayload::V18(EchConfigContents {
            key_config: HpkeKeyConfig {
                config_id: 7,
                kem_id: AadEchoHpke::SUITE.kem,
                public_key: vec![0; 32].into(),
                symmetric_cipher_suites: vec![AadEchoHpke::SUITE.sym],
            },
            maximum_name_length: 64,
            public_name: DnsName::try_from("public.example").unwrap(),
            extensions: vec![],
        }),
        suite: &AAD_ECHO_HPKE,
        tls13_only,
    }
}

fn plaintext_echo_ech_config(tls13_only: bool) -> EchConfig {
    EchConfig {
        config: EchConfigPayload::V18(EchConfigContents {
            key_config: HpkeKeyConfig {
                config_id: 7,
                kem_id: PlaintextEchoHpke::SUITE.kem,
                public_key: vec![0; 32].into(),
                symmetric_cipher_suites: vec![PlaintextEchoHpke::SUITE.sym],
            },
            maximum_name_length: 64,
            public_name: DnsName::try_from("public.example").unwrap(),
            extensions: vec![],
        }),
        suite: &PLAINTEXT_ECHO_HPKE,
        tls13_only,
    }
}

impl Hpke for AadEchoHpke {
    fn seal(
        &self,
        _info: &[u8],
        aad: &[u8],
        _plaintext: &[u8],
        _pub_key: &HpkePublicKey,
    ) -> Result<(EncapsulatedSecret, Vec<u8>), Error> {
        Ok((EncapsulatedSecret(vec![0xee; 4]), aad.to_vec()))
    }

    fn setup_sealer(
        &self,
        _info: &[u8],
        _pub_key: &HpkePublicKey,
    ) -> Result<(EncapsulatedSecret, Box<dyn HpkeSealer + 'static>), Error> {
        Ok((EncapsulatedSecret(vec![0xee; 4]), Box::new(AadEchoSealer)))
    }

    fn open(
        &self,
        _enc: &EncapsulatedSecret,
        _info: &[u8],
        _aad: &[u8],
        _ciphertext: &[u8],
        _secret_key: &HpkePrivateKey,
    ) -> Result<Vec<u8>, Error> {
        Err(Error::General("test HPKE opener is not implemented".into()))
    }

    fn setup_opener(
        &self,
        _enc: &EncapsulatedSecret,
        _info: &[u8],
        _secret_key: &HpkePrivateKey,
    ) -> Result<Box<dyn crate::crypto::hpke::HpkeOpener + 'static>, Error> {
        Err(Error::General("test HPKE opener is not implemented".into()))
    }

    fn generate_key_pair(&self) -> Result<(HpkePublicKey, HpkePrivateKey), Error> {
        Ok((
            HpkePublicKey(vec![0; 32]),
            HpkePrivateKey::from(vec![0; 32]),
        ))
    }

    fn suite(&self) -> HpkeSuite {
        Self::SUITE
    }
}

#[derive(Debug)]
struct AadEchoSealer;

impl HpkeSealer for AadEchoSealer {
    fn seal(&mut self, aad: &[u8], _plaintext: &[u8]) -> Result<Vec<u8>, Error> {
        Ok(aad.to_vec())
    }
}

#[derive(Debug)]
struct PlaintextEchoHpke;

static PLAINTEXT_ECHO_HPKE: PlaintextEchoHpke = PlaintextEchoHpke;

impl PlaintextEchoHpke {
    const SUITE: HpkeSuite = AadEchoHpke::SUITE;
}

impl Hpke for PlaintextEchoHpke {
    fn seal(
        &self,
        _info: &[u8],
        _aad: &[u8],
        plaintext: &[u8],
        _pub_key: &HpkePublicKey,
    ) -> Result<(EncapsulatedSecret, Vec<u8>), Error> {
        Ok((EncapsulatedSecret(vec![0xee; 4]), plaintext.to_vec()))
    }

    fn setup_sealer(
        &self,
        _info: &[u8],
        _pub_key: &HpkePublicKey,
    ) -> Result<(EncapsulatedSecret, Box<dyn HpkeSealer + 'static>), Error> {
        Ok((
            EncapsulatedSecret(vec![0xee; 4]),
            Box::new(PlaintextEchoSealer),
        ))
    }

    fn open(
        &self,
        _enc: &EncapsulatedSecret,
        _info: &[u8],
        _aad: &[u8],
        _ciphertext: &[u8],
        _secret_key: &HpkePrivateKey,
    ) -> Result<Vec<u8>, Error> {
        Err(Error::General("test HPKE opener is not implemented".into()))
    }

    fn setup_opener(
        &self,
        _enc: &EncapsulatedSecret,
        _info: &[u8],
        _secret_key: &HpkePrivateKey,
    ) -> Result<Box<dyn crate::crypto::hpke::HpkeOpener + 'static>, Error> {
        Err(Error::General("test HPKE opener is not implemented".into()))
    }

    fn generate_key_pair(&self) -> Result<(HpkePublicKey, HpkePrivateKey), Error> {
        Ok((
            HpkePublicKey(vec![0; 32]),
            HpkePrivateKey::from(vec![0; 32]),
        ))
    }

    fn suite(&self) -> HpkeSuite {
        Self::SUITE
    }
}

#[derive(Debug)]
struct PlaintextEchoSealer;

impl HpkeSealer for PlaintextEchoSealer {
    fn seal(&mut self, _aad: &[u8], plaintext: &[u8]) -> Result<Vec<u8>, Error> {
        Ok(plaintext.to_vec())
    }
}

fn assert_chrome_108_cipher_suites(cipher_suites: &[CipherSuite]) {
    assert_eq!(cipher_suites.len(), 16);
    assert!(is_grease_u16(u16::from(cipher_suites[0])));
    assert_eq!(
        &cipher_suites[1..],
        &[
            CipherSuite::TLS13_AES_128_GCM_SHA256,
            CipherSuite::TLS13_AES_256_GCM_SHA384,
            CipherSuite::TLS13_CHACHA20_POLY1305_SHA256,
            CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
            CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
            CipherSuite::TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
            CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
            CipherSuite::TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
            CipherSuite::TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
            CipherSuite::TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
            CipherSuite::TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
            CipherSuite::TLS_RSA_WITH_AES_128_GCM_SHA256,
            CipherSuite::TLS_RSA_WITH_AES_256_GCM_SHA384,
            CipherSuite::TLS_RSA_WITH_AES_128_CBC_SHA,
            CipherSuite::TLS_RSA_WITH_AES_256_CBC_SHA,
        ]
    );
}

fn decode_u16_list(payload: &[u8]) -> Vec<u16> {
    let mut offset = 0;
    let len = take_u16(payload, &mut offset) as usize;
    assert_eq!(payload.len(), offset + len);
    assert_eq!(len % 2, 0);
    take(payload, &mut offset, len)
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect()
}

fn decode_u8_list(payload: &[u8]) -> Vec<u8> {
    let mut offset = 0;
    let len = take_u8(payload, &mut offset) as usize;
    assert_eq!(payload.len(), offset + len);
    take(payload, &mut offset, len).to_vec()
}

fn decode_alpn_protocols(payload: &[u8]) -> Vec<Vec<u8>> {
    let mut offset = 0;
    let len = take_u16(payload, &mut offset) as usize;
    assert_eq!(payload.len(), offset + len);
    let end = offset + len;

    let mut protocols = Vec::new();
    while offset < end {
        let protocol_len = take_u8(payload, &mut offset) as usize;
        protocols.push(take(payload, &mut offset, protocol_len).to_vec());
    }

    protocols
}

fn decode_certificate_authorities(payload: &[u8]) -> Vec<Vec<u8>> {
    let mut offset = 0;
    let len = take_u16(payload, &mut offset) as usize;
    assert_eq!(payload.len(), offset + len);
    let end = offset + len;

    let mut authorities = Vec::new();
    while offset < end {
        let authority_len = take_u16(payload, &mut offset) as usize;
        authorities.push(take(payload, &mut offset, authority_len).to_vec());
    }

    authorities
}

fn decode_u8_prefixed_u16_list(payload: &[u8]) -> Vec<u16> {
    let mut offset = 0;
    let len = take_u8(payload, &mut offset) as usize;
    assert_eq!(payload.len(), offset + len);
    assert_eq!(len % 2, 0);
    take(payload, &mut offset, len)
        .chunks_exact(2)
        .map(|chunk| u16::from_be_bytes([chunk[0], chunk[1]]))
        .collect()
}

fn decode_key_shares(payload: &[u8]) -> Vec<(NamedGroup, Vec<u8>)> {
    let mut offset = 0;
    let len = take_u16(payload, &mut offset) as usize;
    assert_eq!(payload.len(), offset + len);
    let end = offset + len;

    let mut shares = Vec::new();
    while offset < end {
        let group = NamedGroup(take_u16(payload, &mut offset));
        let key_exchange_len = take_u16(payload, &mut offset) as usize;
        let key_exchange = take(payload, &mut offset, key_exchange_len).to_vec();
        shares.push((group, key_exchange));
    }

    shares
}

fn is_grease_u16(value: u16) -> bool {
    value & 0x0f0f == 0x0a0a && value >> 8 == value & 0xff
}

fn is_grease_psk_key_exchange_mode(value: u8) -> bool {
    (value as i16 - 0x0b) % 0x1f == 0 && value >= 0x0b
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

const HYBRID_PROVIDER: CryptoProvider = CryptoProvider {
    kx_groups: Cow::Borrowed(&[FAKE_HYBRID, FAKE_KX_GROUP]),
    ..TEST_PROVIDER
};

const CRAFT_CHROME_PROVIDER: CryptoProvider = CryptoProvider {
    kx_groups: Cow::Borrowed(&[
        FAKE_X25519_KX_GROUP,
        FAKE_SECP256R1_KX_GROUP,
        FAKE_SECP384R1_KX_GROUP,
    ]),
    ..TEST_PROVIDER
};

const CRAFT_CHROME_148_PROVIDER: CryptoProvider = CryptoProvider {
    kx_groups: Cow::Borrowed(&[
        FAKE_X25519_MLKEM768_KX_GROUP,
        FAKE_X25519_KX_GROUP,
        FAKE_SECP256R1_KX_GROUP,
        FAKE_SECP384R1_KX_GROUP,
    ]),
    ..TEST_PROVIDER
};

const CRAFTLSMAXXING_PROVIDER: CryptoProvider = CryptoProvider {
    kx_groups: Cow::Borrowed(&[
        FAKE_X25519_MLKEM768_KX_GROUP,
        FAKE_X25519_KX_GROUP,
        FAKE_SECP256R1_KX_GROUP,
        FAKE_SECP384R1_KX_GROUP,
        FAKE_SECP521R1_KX_GROUP,
    ]),
    ..TEST_PROVIDER
};

#[derive(Debug)]
struct IncrementingRandom;

static INCREMENTING_RANDOM: IncrementingRandom = IncrementingRandom;
static NEXT_RANDOM_BYTE: AtomicU8 = AtomicU8::new(0);

impl SecureRandom for IncrementingRandom {
    fn fill(&self, bytes: &mut [u8]) -> Result<(), GetRandomFailed> {
        for byte in bytes {
            *byte = NEXT_RANDOM_BYTE.fetch_add(1, Ordering::Relaxed);
        }

        Ok(())
    }
}

#[derive(Debug)]
struct Chrome148IncrementingRandom;

static CHROME_148_INCREMENTING_RANDOM: Chrome148IncrementingRandom = Chrome148IncrementingRandom;
static CHROME_148_NEXT_RANDOM_BYTE: AtomicU8 = AtomicU8::new(0);

impl SecureRandom for Chrome148IncrementingRandom {
    fn fill(&self, bytes: &mut [u8]) -> Result<(), GetRandomFailed> {
        for byte in bytes {
            *byte = CHROME_148_NEXT_RANDOM_BYTE.fetch_add(1, Ordering::Relaxed);
        }

        Ok(())
    }
}

const FAKE_HYBRID: &FakeHybrid = &FakeHybrid {
    name: NamedGroup(0xfe00),
    classical: NamedGroup(0xfe01),
};
const FAKE_X25519_MLKEM768_KX_GROUP: &FakeHybrid = &FakeHybrid {
    name: NamedGroup::X25519MLKEM768,
    classical: NamedGroup::X25519,
};
const FAKE_KX_GROUP: &dyn SupportedKxGroup = &FakeKeyExchangeGroup(NamedGroup(0xfe01));
const FAKE_X25519_KX_GROUP: &dyn SupportedKxGroup = &FakeKeyExchangeGroup(NamedGroup::X25519);
const FAKE_SECP256R1_KX_GROUP: &dyn SupportedKxGroup = &FakeKeyExchangeGroup(NamedGroup::secp256r1);
const FAKE_SECP384R1_KX_GROUP: &dyn SupportedKxGroup = &FakeKeyExchangeGroup(NamedGroup::secp384r1);
const FAKE_SECP521R1_KX_GROUP: &dyn SupportedKxGroup = &FakeKeyExchangeGroup(NamedGroup::secp521r1);

#[derive(Clone, Copy, Debug)]
pub(crate) struct FakeHybrid {
    name: NamedGroup,
    classical: NamedGroup,
}

impl SupportedKxGroup for FakeHybrid {
    fn start(&self) -> Result<StartedKeyExchange, Error> {
        Ok(StartedKeyExchange::Hybrid(Box::new(*self)))
    }

    fn name(&self) -> NamedGroup {
        self.name
    }
}

impl kx::HybridKeyExchange for FakeHybrid {
    fn component(&self) -> (NamedGroup, &[u8]) {
        (self.classical, KX_PEER_SHARE)
    }

    fn complete_component(self: Box<Self>, peer_pub_key: &[u8]) -> Result<SharedSecret, Error> {
        match peer_pub_key {
            KX_PEER_SHARE => Ok(SharedSecret::from(KX_SHARED_SECRET)),
            _ => Err(Error::from(PeerMisbehaved::InvalidKeyShare)),
        }
    }

    fn as_key_exchange(&self) -> &(dyn kx::ActiveKeyExchange + 'static) {
        FAKE_HYBRID
    }

    fn into_key_exchange(self: Box<Self>) -> Box<dyn kx::ActiveKeyExchange> {
        self
    }
}

impl kx::ActiveKeyExchange for FakeHybrid {
    fn complete(self: Box<Self>, peer: &[u8]) -> Result<SharedSecret, Error> {
        match peer {
            KX_PEER_SHARE => Ok(SharedSecret::from(KX_SHARED_SECRET)),
            _ => Err(Error::from(PeerMisbehaved::InvalidKeyShare)),
        }
    }

    fn pub_key(&self) -> &[u8] {
        KX_PEER_SHARE
    }

    fn group(&self) -> NamedGroup {
        self.name
    }
}

const KX_PEER_SHARE: &[u8] = b"KxPeerShareKxPeerShareKxPeerShare";
const KX_SHARED_SECRET: &[u8] = b"KxSharedSecretKxSharedSecret";

fn roots() -> RootCertStore {
    let mut r = RootCertStore::empty();
    r.add(CertificateDer::from_slice(include_bytes!(
        "../../../test-ca/rsa-2048/ca.der"
    )))
    .unwrap();
    r
}
