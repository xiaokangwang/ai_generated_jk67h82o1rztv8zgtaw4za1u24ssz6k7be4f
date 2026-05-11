use alloc::boxed::Box;
use alloc::vec;
use alloc::vec::Vec;
use core::iter;

use pki_types::{DnsName, EchConfigListBytes, FipsStatus, ServerName};
use subtle::ConstantTimeEq;

use super::config::ClientConfig;
use super::{Retrieved, Tls13Session, tls13};
use crate::common_state::Protocol;
use crate::craft::EchPaddingStyle;
use crate::crypto::cipher::Payload;
use crate::crypto::hash::Hash;
use crate::crypto::hpke::{
    EncapsulatedSecret, Hpke, HpkeKem, HpkePublicKey, HpkeSealer, HpkeSuite,
    HpkeSymmetricCipherSuite,
};
use crate::crypto::{CipherSuite, SecureRandom};
use crate::enums::ProtocolVersion;
use crate::error::{EncryptedClientHelloError, Error, PeerMisbehaved, RejectedEch};
use crate::hash_hs::{HandshakeHash, HandshakeHashBuffer};
use crate::log::{debug, trace, warn};
use crate::msgs::{
    ClientExtensions, ClientHelloPayload, Codec, EchConfigContents, EchConfigPayload, Encoding,
    EncryptedClientHello, EncryptedClientHelloOuter, ExtensionType, HandshakeAlignedProof,
    HandshakeMessagePayload, HandshakePayload, HelloRetryRequest, HpkeKeyConfig, Message,
    MessagePayload, PresharedKeyBinder, PresharedKeyOffer, Random, Reader, ServerHelloPayload,
    ServerNamePayload, SizedPayload,
};
use crate::tls13::Tls13CipherSuite;
use crate::tls13::key_schedule::{
    KeyScheduleEarlyClient, KeyScheduleHandshakeStart, server_ech_hrr_confirmation_secret,
};

/// Controls how Encrypted Client Hello (ECH) is used in a client handshake.
#[non_exhaustive]
#[derive(Clone, Debug)]
pub enum EchMode {
    /// ECH is enabled and the ClientHello will be encrypted based on the provided
    /// configuration.
    Enable(EchConfig),

    /// No ECH configuration is available but the client should act as though it were.
    ///
    /// This is an anti-ossification measure, sometimes referred to as "GREASE"[^0].
    /// [^0]: <https://www.rfc-editor.org/rfc/rfc8701>
    Grease(EchGreaseConfig),
}

impl EchMode {
    /// Returns true if the ECH mode will use a FIPS approved HPKE suite.
    pub fn fips(&self) -> FipsStatus {
        match self {
            Self::Enable(ech_config) => ech_config.suite.fips(),
            Self::Grease(grease_config) => grease_config.suite.fips(),
        }
    }

    pub(crate) fn tls13_only(&self) -> bool {
        match self {
            Self::Enable(ech_config) => ech_config.tls13_only,
            Self::Grease(grease_config) => grease_config.tls13_only,
        }
    }
}

impl From<EchConfig> for EchMode {
    fn from(config: EchConfig) -> Self {
        Self::Enable(config)
    }
}

impl From<EchGreaseConfig> for EchMode {
    fn from(config: EchGreaseConfig) -> Self {
        Self::Grease(config)
    }
}

/// Configuration for performing encrypted client hello.
///
/// Note: differs from the protocol-encoded EchConfig (`EchConfigMsg`).
#[derive(Clone, Debug)]
pub struct EchConfig {
    /// The selected EchConfig.
    pub(crate) config: EchConfigPayload,

    /// An HPKE instance corresponding to a suite from the `config` we have selected as
    /// a compatible choice.
    pub(crate) suite: &'static dyn Hpke,

    /// Whether offering ECH also disables TLS 1.2 in the outer handshake.
    pub(crate) tls13_only: bool,
}

impl EchConfig {
    /// Construct an EchConfig by selecting a ECH config from the provided bytes that is compatible
    /// with one of the given HPKE suites.
    ///
    /// The config list bytes should be sourced from a DNS-over-HTTPS lookup resolving the `HTTPS`
    /// resource record for the host name of the server you wish to connect via ECH,
    /// and extracting the ECH configuration from the `ech` parameter. The extracted bytes should
    /// be base64 decoded to yield the `EchConfigListBytes` you provide to rustls.
    ///
    /// One of the provided ECH configurations must be compatible with the HPKE provider's supported
    /// suites or an error will be returned.
    ///
    /// See the [`ech-client.rs`] example for a complete example of fetching ECH configs from DNS.
    ///
    /// [`ech-client.rs`]: https://github.com/rustls/rustls/blob/main/examples/src/bin/ech-client.rs
    pub fn new(
        ech_config_list: EchConfigListBytes<'_>,
        hpke_suites: &[&'static dyn Hpke],
    ) -> Result<Self, Error> {
        let ech_configs = Vec::<EchConfigPayload>::read(&mut Reader::new(&ech_config_list))
            .map_err(|_| {
                Error::InvalidEncryptedClientHello(EncryptedClientHelloError::InvalidConfigList)
            })?;

        Self::new_for_configs(ech_configs, hpke_suites)
    }

    /// Build an EchConfig for retrying ECH using a retry config from a server's previous rejection
    ///
    /// Returns an error if the server provided no retry configurations in `RejectedEch`, or if
    /// none of the retry configurations are compatible with the supported `hpke_suites`.
    pub fn for_retry(
        rejection: RejectedEch,
        hpke_suites: &[&'static dyn Hpke],
    ) -> Result<Self, Error> {
        let Some(configs) = rejection.retry_configs else {
            return Err(EncryptedClientHelloError::NoCompatibleConfig.into());
        };

        Self::new_for_configs(configs, hpke_suites)
    }

    /// Configure whether enabling ECH forces the whole handshake to offer TLS 1.3 only.
    ///
    /// The default is `true`, which preserves rustls' historical ECH behavior. Set this to
    /// `false` to allow a browser-style outer ClientHello that can fall back to TLS 1.2 if
    /// ECH is not accepted.
    pub fn with_tls13_only(mut self, tls13_only: bool) -> Self {
        self.tls13_only = tls13_only;
        self
    }

    pub(super) fn state(
        &self,
        server_name: ServerName<'static>,
        protocol: Protocol,
        config: &ClientConfig,
    ) -> Result<EchState, Error> {
        EchState::new(
            self,
            server_name.clone(),
            protocol,
            !config
                .resolver()
                .supported_certificate_types()
                .is_empty(),
            config.provider().secure_random,
            config.enable_sni,
        )
    }

    /// Compute the HPKE `SetupBaseS` `info` parameter for this ECH configuration.
    ///
    /// See <https://datatracker.ietf.org/doc/html/rfc9849#section-6.1>.
    pub(crate) fn hpke_info(&self) -> Vec<u8> {
        let mut info = Vec::with_capacity(128);
        // "tls ech" || 0x00 || ECHConfig
        info.extend_from_slice(b"tls ech\0");
        self.config.encode(&mut info);
        info
    }

    fn new_for_configs(
        ech_configs: Vec<EchConfigPayload>,
        hpke_suites: &[&'static dyn Hpke],
    ) -> Result<Self, Error> {
        for (i, config) in ech_configs.iter().enumerate() {
            let contents = match config {
                EchConfigPayload::V18(contents) => contents,
                EchConfigPayload::Unknown { version, .. } => {
                    warn!("ECH config {} has unsupported version {:?}", i + 1, version);
                    continue; // Unsupported version.
                }
            };

            if contents.has_unknown_mandatory_extension() || contents.has_duplicate_extension() {
                warn!("ECH config has duplicate, or unknown mandatory extensions: {contents:?}",);
                continue; // Unsupported, or malformed extensions.
            }

            let key_config = &contents.key_config;
            for cipher_suite in &key_config.symmetric_cipher_suites {
                if cipher_suite.aead_id.tag_len().is_none() {
                    continue; // Unsupported EXPORT_ONLY AEAD cipher suite.
                }

                let suite = HpkeSuite {
                    kem: key_config.kem_id,
                    sym: *cipher_suite,
                };
                if let Some(hpke) = hpke_suites
                    .iter()
                    .find(|hpke| hpke.suite() == suite)
                {
                    debug!(
                        "selected ECH config ID {:?} suite {:?} public_name {:?}",
                        key_config.config_id, suite, contents.public_name
                    );
                    return Ok(Self {
                        config: config.clone(),
                        suite: *hpke,
                        tls13_only: true,
                    });
                }
            }
        }

        Err(EncryptedClientHelloError::NoCompatibleConfig.into())
    }
}

/// Configuration for GREASE Encrypted Client Hello.
#[derive(Clone, Debug)]
pub struct EchGreaseConfig {
    pub(crate) suite: &'static dyn Hpke,
    pub(crate) placeholder_key: HpkePublicKey,
    pub(crate) tls13_only: bool,
}

impl EchGreaseConfig {
    /// Construct a GREASE ECH configuration.
    ///
    /// This configuration is used when the client wishes to offer ECH to prevent ossification,
    /// but doesn't have a real ECH configuration to use for the remote server. In this case
    /// a placeholder or "GREASE"[^0] extension is used.
    ///
    /// Returns an error if the HPKE provider does not support the given suite.
    ///
    /// [^0]: <https://www.rfc-editor.org/rfc/rfc8701>
    pub fn new(suite: &'static dyn Hpke, placeholder_key: HpkePublicKey) -> Self {
        Self {
            suite,
            placeholder_key,
            tls13_only: true,
        }
    }

    /// Configure whether GREASE ECH also forces the whole handshake to offer TLS 1.3 only.
    pub fn with_tls13_only(mut self, tls13_only: bool) -> Self {
        self.tls13_only = tls13_only;
        self
    }

    /// Build a GREASE ECH extension based on the placeholder configuration.
    ///
    /// See <https://datatracker.ietf.org/doc/html/rfc9849#name-grease-ech> for
    /// more information.
    pub(crate) fn grease_ext(
        &self,
        secure_random: &'static dyn SecureRandom,
        protocol: Protocol,
        inner_name: ServerName<'static>,
        outer_hello: &ClientHelloPayload,
    ) -> Result<EncryptedClientHello, Error> {
        trace!("Preparing GREASE ECH extension");

        // Pick a random config id.
        let mut config_id: [u8; 1] = [0; 1];
        secure_random.fill(&mut config_id[..])?;

        let suite = self.suite.suite();

        // Construct a dummy ECH state - we don't have a real ECH config from a server since
        // this is for GREASE.
        let mut grease_state = EchState::new(
            &EchConfig {
                config: EchConfigPayload::V18(EchConfigContents {
                    key_config: HpkeKeyConfig {
                        config_id: config_id[0],
                        kem_id: HpkeKem::DHKEM_P256_HKDF_SHA256,
                        public_key: SizedPayload::from(self.placeholder_key.0.clone()),
                        symmetric_cipher_suites: vec![suite.sym],
                    },
                    maximum_name_length: 0,
                    public_name: DnsName::try_from("filler").unwrap(),
                    extensions: Vec::default(),
                }),
                suite: self.suite,
                tls13_only: self.tls13_only,
            },
            inner_name,
            protocol,
            false,
            secure_random,
            false, // Does not matter if we enable/disable SNI here. Inner hello is not used.
        )?;

        // Construct an inner hello using the outer hello - this allows us to know the size of
        // dummy payload we should use for the GREASE extension.
        let encoded_inner_hello =
            grease_state.encode_inner_hello(outer_hello, None, None, EchPaddingStyle::Standard);

        // Generate a payload of random data equivalent in length to a real inner hello.
        let payload_len = encoded_inner_hello.len()
            + suite
                .sym
                .aead_id
                .tag_len()
                // Safety: we have confirmed the AEAD is supported when building the config. All
                //  supported AEADs have a tag length.
                .unwrap();
        let mut payload = vec![0; payload_len];
        secure_random.fill(&mut payload)?;

        // Return the GREASE extension.
        Ok(EncryptedClientHello::Outer(EncryptedClientHelloOuter {
            cipher_suite: suite.sym,
            config_id: config_id[0],
            enc: SizedPayload::from(Payload::new(grease_state.enc.0)),
            payload: SizedPayload::from(Payload::new(payload)),
        }))
    }
}

/// An enum representing ECH offer status.
#[non_exhaustive]
#[derive(Debug, Default, Clone, Copy, Eq, PartialEq)]
pub enum EchStatus {
    /// ECH was not offered - it is a normal TLS handshake.
    #[default]
    NotOffered,
    /// GREASE ECH was sent. This is not considered offering ECH.
    Grease,
    /// ECH was offered but we do not yet know whether the offer was accepted or rejected.
    Offered,
    /// ECH was offered and the server accepted.
    Accepted,
    /// ECH was offered and the server rejected.
    Rejected,
}

/// Contextual data for a TLS client handshake that has offered encrypted client hello (ECH).
pub(crate) struct EchState {
    // The public DNS name from the ECH configuration we've chosen - this is included as the SNI
    // value for the "outer" client hello. It can only be a DnsName, not an IP address.
    pub(crate) outer_name: DnsName<'static>,
    // If we're resuming in the inner hello, this is the early key schedule to use for encrypting
    // early data if the ECH offer is accepted.
    pub(crate) early_data_key_schedule: Option<KeyScheduleEarlyClient>,
    // A random value we use for the inner hello.
    pub(crate) inner_hello_random: Random,
    // A transcript buffer maintained for the inner hello. Once ECH is confirmed we switch to
    // using this transcript for the handshake.
    pub(crate) inner_hello_transcript: HandshakeHashBuffer,
    // A source of secure random data.
    secure_random: &'static dyn SecureRandom,
    // The top level protocol
    protocol: Protocol,
    // An HPKE sealer context that can be used for encrypting ECH data.
    sender: Box<dyn HpkeSealer>,
    // The ID of the ECH configuration we've chosen - this is included in the outer ECH extension.
    config_id: u8,
    // The private server name we'll use for the inner protected hello.
    inner_name: ServerName<'static>,
    // The advertised maximum name length from the ECH configuration we've chosen - this is used
    // for padding calculations.
    maximum_name_length: u8,
    // A supported symmetric cipher suite from the ECH configuration we've chosen - this is
    // included in the outer ECH extension.
    cipher_suite: HpkeSymmetricCipherSuite,
    // A secret encapsulated to the public key of the remote server. This is included in the
    // outer ECH extension for non-retry outer hello messages.
    enc: EncapsulatedSecret,
    // Whether the inner client hello should contain a server name indication (SNI) extension.
    enable_sni: bool,
    // The extensions sent in the inner hello.
    sent_extensions: Vec<ExtensionType>,
}

impl EchState {
    pub(crate) fn new(
        config: &EchConfig,
        inner_name: ServerName<'static>,
        protocol: Protocol,
        client_auth_enabled: bool,
        secure_random: &'static dyn SecureRandom,
        enable_sni: bool,
    ) -> Result<Self, Error> {
        let EchConfigPayload::V18(config_contents) = &config.config else {
            // the public EchConfig::new() constructor ensures we only have supported
            // configurations.
            unreachable!("ECH config version mismatch");
        };
        let key_config = &config_contents.key_config;

        // Encapsulate a secret for the server's public key, and set up a sender context
        // we can use to seal messages.
        let (enc, sender) = config.suite.setup_sealer(
            &config.hpke_info(),
            &HpkePublicKey(key_config.public_key.to_vec()),
        )?;

        // Start a new transcript buffer for the inner hello.
        let mut inner_hello_transcript = HandshakeHashBuffer::new();
        if client_auth_enabled {
            inner_hello_transcript.set_client_auth_enabled();
        }

        Ok(Self {
            outer_name: config_contents.public_name.clone(),
            early_data_key_schedule: None,
            inner_hello_random: Random::new(secure_random)?,
            inner_hello_transcript,
            secure_random,
            sender,
            config_id: key_config.config_id,
            inner_name,
            maximum_name_length: config_contents.maximum_name_length,
            cipher_suite: config.suite.suite().sym,
            protocol,
            enc,
            enable_sni,
            sent_extensions: Vec::new(),
        })
    }

    /// Construct a ClientHelloPayload offering ECH.
    ///
    /// An outer hello, with a protected inner hello for the `inner_name` will be returned, and the
    /// ECH context will be updated to reflect the inner hello that was offered.
    ///
    /// If `retry_req` is `Some`, then the outer hello will be constructed for a hello retry request.
    ///
    /// If `resuming` is `Some`, then the inner hello will be constructed for a resumption handshake.
    pub(crate) fn ech_hello(
        &mut self,
        mut outer_hello: ClientHelloPayload,
        retry_req: Option<&HelloRetryRequest>,
        resuming: Option<&Retrieved<&Tls13Session>>,
        padding_style: EchPaddingStyle,
    ) -> Result<ClientHelloPayload, Error> {
        trace!(
            "Preparing ECH offer {}",
            if retry_req.is_some() { "for retry" } else { "" }
        );

        // Construct the encoded inner hello and update the transcript.
        let encoded_inner_hello =
            self.encode_inner_hello(&outer_hello, retry_req, resuming, padding_style);

        // Complete the ClientHelloOuterAAD with an ech extension, the payload should be a placeholder
        // of size L, all zeroes. L == length of encrypting encoded client hello inner w/ the selected
        // HPKE AEAD. (sum of plaintext + tag length, typically).
        let payload_len = encoded_inner_hello.len()
            + self
                .cipher_suite
                .aead_id
                .tag_len()
                // Safety: we've already verified this AEAD is supported when loading the config
                // that was used to create the ECH context. All supported AEADs have a tag length.
                .unwrap();

        // Outer hello's created in response to a hello retry request omit the enc value.
        let enc = match retry_req.is_some() {
            true => Vec::default(),
            false => self.enc.0.clone(),
        };

        fn outer_hello_ext(ctx: &EchState, enc: Vec<u8>, payload: Vec<u8>) -> EncryptedClientHello {
            EncryptedClientHello::Outer(EncryptedClientHelloOuter {
                cipher_suite: ctx.cipher_suite,
                config_id: ctx.config_id,
                enc: SizedPayload::from(Payload::new(enc)),
                payload: SizedPayload::from(Payload::new(payload)),
            })
        }

        fn set_outer_hello_ext(outer_hello: &mut ClientHelloPayload, ech: EncryptedClientHello) {
            let mut payload = Vec::new();
            ech.encode(&mut payload);
            outer_hello.encrypted_client_hello = Some(ech);

            let Some(craft_extensions) = outer_hello.craft_extensions.as_mut() else {
                return;
            };

            if let Some(existing) = craft_extensions
                .iter_mut()
                .find(|ext| ext.extension_type() == ExtensionType::EncryptedClientHello)
            {
                *existing = crate::craft::CraftClientExtension::raw(
                    ExtensionType::EncryptedClientHello,
                    payload,
                );
                return;
            }

            let insert_at = craft_extensions
                .iter()
                .position(|ext| {
                    matches!(
                        ext.extension_type(),
                        ExtensionType::Padding | ExtensionType::PreSharedKey
                    )
                })
                .unwrap_or(craft_extensions.len());
            craft_extensions.insert(
                insert_at,
                crate::craft::CraftClientExtension::raw(
                    ExtensionType::EncryptedClientHello,
                    payload,
                ),
            );
        }

        // The outer handshake is not permitted to resume a session. If we're resuming in the
        // inner handshake we remove the PSK extension from the outer hello, replacing it
        // with a GREASE PSK to implement the "ClientHello Malleability Mitigation" mentioned
        // in 10.12.3.
        if let Some(psk_offer) = outer_hello.preshared_key_offer.as_mut() {
            self.grease_psk(psk_offer)?;
        }

        // To compute the encoded AAD we add a placeholder extension with an empty payload.
        set_outer_hello_ext(
            &mut outer_hello,
            outer_hello_ext(self, enc.clone(), vec![0; payload_len]),
        );

        // Next we compute the proper extension payload.
        let payload = self
            .sender
            .seal(&outer_hello.get_encoding(), &encoded_inner_hello)?;

        // And then we replace the placeholder extension with the real one.
        set_outer_hello_ext(&mut outer_hello, outer_hello_ext(self, enc, payload));

        Ok(outer_hello)
    }

    /// Confirm whether an ECH offer was accepted based on examining the server hello.
    pub(crate) fn confirm_acceptance(
        self,
        ks: &KeyScheduleHandshakeStart,
        server_hello: &ServerHelloPayload,
        server_hello_encoded: &Payload<'_>,
        hash: &'static dyn Hash,
    ) -> Result<Option<EchAccepted>, Error> {
        // Start the inner transcript hash now that we know the hash algorithm to use.
        let inner_transcript = self
            .inner_hello_transcript
            .start_hash(hash);

        // Fork the transcript that we've started with the inner hello to use for a confirmation step.
        // We need to preserve the original inner_transcript to use if this confirmation succeeds.
        let mut confirmation_transcript = inner_transcript.clone();

        // Add the server hello confirmation - this is computed by altering the received
        // encoding rather than reencoding it.
        confirmation_transcript
            .add_message(&Self::server_hello_conf(server_hello, server_hello_encoded));

        // Derive a confirmation secret from the inner hello random and the confirmation transcript.
        let derived = ks.server_ech_confirmation_secret(
            self.inner_hello_random.0.as_ref(),
            confirmation_transcript.current_hash(),
        );

        // Check that first 8 digits of the derived secret match the last 8 digits of the original
        // server random. This match signals that the server accepted the ECH offer.
        // Indexing safety: Random is [0; 32] by construction.

        match ConstantTimeEq::ct_eq(derived.as_ref(), server_hello.random.0[24..].as_ref()).into() {
            true => {
                trace!("ECH accepted by server");
                Ok(Some(EchAccepted {
                    transcript: inner_transcript,
                    random: self.inner_hello_random,
                    sent_extensions: self.sent_extensions,
                }))
            }
            false => {
                trace!("ECH rejected by server");
                Ok(None)
            }
        }
    }

    pub(crate) fn confirm_hrr_acceptance(
        &self,
        hrr: &HelloRetryRequest,
        cs: &Tls13CipherSuite,
    ) -> Result<bool, Error> {
        // The client checks for the "encrypted_client_hello" extension.
        let ech_conf = match &hrr.encrypted_client_hello {
            // If none is found, the server has implicitly rejected ECH.
            None => return Ok(false),
            // Otherwise, if it has a length other than 8, the client aborts the
            // handshake with a "decode_error" alert.
            Some(ech_conf) if ech_conf.bytes().len() != 8 => {
                return Err(PeerMisbehaved::IllegalHelloRetryRequestWithInvalidEch.into());
            }
            Some(ech_conf) => ech_conf,
        };

        // Otherwise the client computes hrr_accept_confirmation as described in Section
        // 7.2.1
        let confirmation_transcript = self.inner_hello_transcript.clone();
        let mut confirmation_transcript =
            confirmation_transcript.start_hash(cs.common.hash_provider);
        confirmation_transcript.rollup_for_hrr();
        confirmation_transcript.add_message(&Self::hello_retry_request_conf(hrr));

        let derived = server_ech_hrr_confirmation_secret(
            cs.hkdf_provider,
            &self.inner_hello_random.0,
            confirmation_transcript.current_hash(),
        );

        match ConstantTimeEq::ct_eq(derived.as_ref(), ech_conf.bytes()).into() {
            true => {
                trace!("ECH accepted by server in hello retry request");
                Ok(true)
            }
            false => {
                trace!("ECH rejected by server in hello retry request");
                Ok(false)
            }
        }
    }

    /// Update the ECH context inner hello transcript based on a received hello retry request message.
    ///
    /// This will start the in-progress transcript using the given `hash`, convert it into an HRR
    /// buffer, and then add the hello retry message `m`.
    pub(crate) fn transcript_hrr_update(
        &mut self,
        hash: &'static dyn Hash,
        m: &Message<'_>,
        proof: &HandshakeAlignedProof,
    ) {
        trace!("Updating ECH inner transcript for HRR");

        let inner_transcript = self
            .inner_hello_transcript
            .clone()
            .start_hash(hash);

        let mut inner_transcript_buffer = inner_transcript.into_hrr_buffer(proof);
        inner_transcript_buffer.add_message(m);
        self.inner_hello_transcript = inner_transcript_buffer;
    }

    // 5.1 "Encoding the ClientHelloInner"
    fn encode_inner_hello(
        &mut self,
        outer_hello: &ClientHelloPayload,
        retryreq: Option<&HelloRetryRequest>,
        resuming: Option<&Retrieved<&Tls13Session>>,
        padding_style: EchPaddingStyle,
    ) -> Vec<u8> {
        // Start building an inner hello using the outer_hello as a template.
        let mut inner_hello = ClientHelloPayload {
            // Some information is copied over as-is.
            client_version: outer_hello.client_version,

            // Set the inner hello random to the one we generated when creating the ECH state.
            // We hold on to the inner_hello_random in the ECH state to use later for confirming
            // whether ECH was accepted or not.
            random: self.inner_hello_random,
            session_id: outer_hello.session_id,

            // We remove the empty renegotiation info SCSV from the outer hello's ciphersuite.
            // ECH ClientHelloInner is TLS 1.3-only, so it carries only TLS 1.3 cipher suites and
            // GREASE. BoringSSL follows the same split: ClientHelloOuter keeps the TLS 1.2
            // fallback cipher surface, but ClientHelloInner does not.
            cipher_suites: outer_hello
                .cipher_suites
                .iter()
                .filter(|cs| is_tls13_or_grease_cipher_suite(**cs))
                .copied()
                .collect(),
            compression_methods: outer_hello.compression_methods.clone(),

            // We will build up the included extensions ourselves.
            extensions: Box::new(ClientExtensions::default()),
            craft_extensions: None,
        };

        inner_hello.order_seed = outer_hello.order_seed;

        // The inner hello will always have an inner variant of the ECH extension added.
        // See Section 6.1 rule 4.
        inner_hello.encrypted_client_hello = Some(EncryptedClientHello::Inner);

        let inner_sni = match &self.inner_name {
            // The inner hello only gets a SNI value if enable_sni is true and the inner name
            // is a domain name (not an IP address).
            ServerName::DnsName(dns_name) if self.enable_sni => Some(dns_name),
            _ => None,
        };

        // Now we consider each of the outer hello's extensions - we can either:
        // 1. Omit the extension if it isn't appropriate (e.g. is a TLS 1.2 extension).
        // 2. Add the extension to the inner hello as-is.
        // 3. Compress the extension, by collecting it into a list of to-be-compressed
        //    extensions we'll handle separately.
        let outer_extensions = client_hello_extension_encoding_order(outer_hello);
        let mut compressed_exts = Vec::with_capacity(outer_extensions.len());
        for ext in outer_extensions {
            // Some outer hello extensions are only useful in the context where a TLS 1.3
            // connection allows TLS 1.2. This isn't the case for ECH so we skip adding them
            // to the inner hello.
            if matches!(
                ext,
                ExtensionType::ExtendedMasterSecret
                    | ExtensionType::SessionTicket
                    | ExtensionType::ECPointFormats
            ) {
                continue;
            }

            if ext == ExtensionType::ServerName {
                // We may want to replace the outer hello SNI with our own inner hello specific SNI.
                if let Some(sni_value) = inner_sni {
                    inner_hello.server_name = Some(ServerNamePayload::from(sni_value));
                }
                // We don't want to add, or compress, the SNI from the outer hello.
                continue;
            }

            if matches!(
                ext,
                ExtensionType::EncryptedClientHello
                    | ExtensionType::EncryptedClientHelloOuterExtensions
            ) {
                // ClientHelloInner has its own ECH marker. Never copy the outer real/placeholder
                // ECH extension into the encrypted inner.
                continue;
            }

            let should_compress = ext.ech_compress()
                && !(ext == ExtensionType::SupportedVersions
                    && outer_supported_versions_has_tls12(outer_hello));

            // Compressed extensions need to be put aside to include in one contiguous block.
            // Uncompressed extensions get added directly to the inner hello. For crafted outer
            // hellos, first copy the crafted wire value into the typed inner hello model when we
            // can decode it, so the transcript we compute matches either the encoded inner value
            // or what the server reconstructs from encrypted_client_hello_outer_extensions.
            let cloned_crafted = clone_crafted_outer_extension(&mut inner_hello, outer_hello, ext);
            if cloned_crafted {
                if should_compress {
                    compressed_exts.push(ext);
                }
                continue;
            }

            if should_compress && outer_hello.craft_extensions.is_none() {
                inner_hello.clone_one(outer_hello, ext);
                compressed_exts.push(ext);
                continue;
            }

            inner_hello.clone_one(outer_hello, ext);
        }

        // ECH ClientHelloInner is only valid for TLS 1.3 and later. The outer hello may
        // deliberately retain TLS 1.2 for browser-style fallback, but the protected inner
        // handshake must not offer it.
        if let Some(supported_versions) = inner_hello.supported_versions.as_mut() {
            supported_versions.tls12 = false;
        }

        // We've added all the uncompressed extensions. Now we need to add the contiguous
        // block of to-be-compressed extensions.
        inner_hello.contiguous_extensions = compressed_exts.clone();

        // Note which extensions we're sending in the inner hello. This may differ from
        // the outer hello (e.g. the inner hello may omit SNI while the outer hello will
        // always have the ECH cover name in SNI).
        self.sent_extensions = inner_hello.collect_used();

        // If we're resuming, we need to update the PSK binder in the inner hello.
        if let Some(resuming) = resuming.as_ref() {
            let mut chp = HandshakeMessagePayload(HandshakePayload::ClientHello(inner_hello));

            let key_schedule =
                KeyScheduleEarlyClient::new(self.protocol, resuming.suite, resuming.secret.bytes());
            tls13::fill_in_psk_binder(&key_schedule, &self.inner_hello_transcript, &mut chp);
            self.early_data_key_schedule = Some(key_schedule);

            // fill_in_psk_binder works on an owned HandshakeMessagePayload, so we need to
            // extract our inner hello back out of it to retain ownership.
            inner_hello = match chp.0 {
                HandshakePayload::ClientHello(chp) => chp,
                // Safety: we construct the HMP above and know its type unconditionally.
                _ => unreachable!(),
            };
        }

        trace!("ECH Inner Hello: {inner_hello:#?}");

        // Encode the inner hello according to the rules required for ECH. This differs
        // from the standard encoding in several ways. Notably this is where we will
        // replace the block of contiguous to-be-compressed extensions with a marker.
        let mut encoded_hello = inner_hello.ech_inner_encoding(compressed_exts);

        // Calculate padding
        // max_name_len = L
        let max_name_len = usize::from(self.maximum_name_length);

        let name_padding_len = match &inner_hello.server_name {
            Some(ServerNamePayload::SingleDnsName(name)) => {
                // name.len() = D
                // max(0, L - D)
                Ord::max(0, max_name_len.saturating_sub(name.as_ref().len()))
            }
            // L + 9
            // "This is the length of a "server_name" extension with an L-byte name."
            _ => max_name_len + 9,
        };
        encoded_hello.extend(iter::repeat_n(0, name_padding_len));

        // Let L be the length of the EncodedClientHelloInner with all the padding computed so far.
        // The standard path rounds L up to a multiple of 32. NSS/Firefox pads L to 31 mod 32.
        let padding_len = match padding_style {
            EchPaddingStyle::Standard => 31 - ((encoded_hello.len() - 1) % 32),
            EchPaddingStyle::Nss => 31 - (encoded_hello.len() % 32),
        };
        encoded_hello.extend(iter::repeat_n(0, padding_len));

        // Construct the inner hello message that will be used for the transcript.
        let inner_hello_msg = Message {
            version: match retryreq {
                // <https://datatracker.ietf.org/doc/html/rfc8446#section-5.1>:
                // "This value MUST be set to 0x0303 for all records generated
                //  by a TLS 1.3 implementation ..."
                Some(_) => ProtocolVersion::TLSv1_2,
                // "... other than an initial ClientHello (i.e., one not
                // generated after a HelloRetryRequest), where it MAY also be
                // 0x0301 for compatibility purposes"
                //
                // (retryreq == None means we're in the "initial ClientHello" case)
                None => ProtocolVersion::TLSv1_0,
            },
            payload: MessagePayload::handshake(HandshakeMessagePayload(
                HandshakePayload::ClientHello(inner_hello),
            )),
        };

        // Update the inner transcript buffer with the inner hello message.
        self.inner_hello_transcript
            .add_message(&inner_hello_msg);

        encoded_hello
    }

    // See https://datatracker.ietf.org/doc/html/rfc9849#name-grease-psk
    fn grease_psk(&self, psk_offer: &mut PresharedKeyOffer) -> Result<(), Error> {
        for ident in psk_offer.identities.iter_mut() {
            // "For each PSK identity advertised in the ClientHelloInner, the
            // client generates a random PSK identity with the same length."
            match ident.identity.as_mut() {
                Some(ident) => self.secure_random.fill(ident)?,
                None => unreachable!(),
            }

            // "It also generates a random, 32-bit, unsigned integer to use as
            // the obfuscated_ticket_age."
            let mut ticket_age = [0_u8; 4];
            self.secure_random
                .fill(&mut ticket_age)?;
            ident.obfuscated_ticket_age = u32::from_be_bytes(ticket_age);
        }

        // "Likewise, for each inner PSK binder, the client generates a random string
        // of the same length."
        psk_offer.binders = psk_offer
            .binders
            .iter()
            .map(|old_binder| {
                // We can't access the wrapped binder PresharedKeyBinder's PayloadU8 mutably,
                // so we construct new PresharedKeyBinder's from scratch with the same length.
                let mut new_binder = vec![0; old_binder.as_ref().len()];
                self.secure_random
                    .fill(&mut new_binder)?;
                Ok::<PresharedKeyBinder, Error>(PresharedKeyBinder::from(new_binder))
            })
            .collect::<Result<_, _>>()?;
        Ok(())
    }

    fn server_hello_conf(
        server_hello: &ServerHelloPayload,
        server_hello_encoded: &Payload<'_>,
    ) -> Message<'static> {
        // The confirmation is computed over the server hello, which has had
        // its `random` field altered to zero the final 8 bytes.
        //
        // nb. we don't require that we can round-trip a `ServerHelloPayload`, to
        // allow for efficiency in its in-memory representation.  That means
        // we operate here on the received encoding, as the confirmation needs
        // to be computed on that.
        let mut encoded = server_hello_encoded.clone().into_vec();
        encoded[SERVER_HELLO_ECH_CONFIRMATION_SPAN].fill(0x00);

        Message {
            version: ProtocolVersion::TLSv1_3,
            payload: MessagePayload::Handshake {
                encoded: Payload::Owned(encoded),
                parsed: HandshakeMessagePayload(HandshakePayload::ServerHello(
                    server_hello.clone(),
                )),
            },
        }
    }

    fn hello_retry_request_conf(retry_req: &HelloRetryRequest) -> Message<'_> {
        Self::ech_conf_message(HandshakeMessagePayload(
            HandshakePayload::HelloRetryRequest(retry_req.clone()),
        ))
    }

    fn ech_conf_message(hmp: HandshakeMessagePayload<'_>) -> Message<'_> {
        let mut hmp_encoded = Vec::new();
        hmp.payload_encode(&mut hmp_encoded, Encoding::EchConfirmation);
        Message {
            version: ProtocolVersion::TLSv1_3,
            payload: MessagePayload::Handshake {
                encoded: Payload::new(hmp_encoded),
                parsed: hmp,
            },
        }
    }
}

fn clone_crafted_outer_extension(
    inner_hello: &mut ClientHelloPayload,
    outer_hello: &ClientHelloPayload,
    typ: ExtensionType,
) -> bool {
    let Some(payload) = outer_hello
        .craft_extensions
        .as_ref()
        .and_then(|extensions| {
            extensions
                .iter()
                .find(|extension| extension.extension_type() == typ)
                .and_then(|extension| extension.raw_payload())
        })
    else {
        return false;
    };

    let extension_len = 4usize.saturating_add(payload.len());
    let Ok(extension_len) = u16::try_from(extension_len) else {
        return false;
    };
    let Ok(payload_len) = u16::try_from(payload.len()) else {
        return false;
    };

    let mut encoded = Vec::with_capacity(2 + usize::from(extension_len));
    encoded.extend_from_slice(&extension_len.to_be_bytes());
    typ.encode(&mut encoded);
    encoded.extend_from_slice(&payload_len.to_be_bytes());
    encoded.extend_from_slice(payload);

    let Ok(decoded) = ClientExtensions::read_bytes(&encoded).map(ClientExtensions::into_owned)
    else {
        return false;
    };
    inner_hello.clone_one(&decoded, typ);
    true
}

fn client_hello_extension_encoding_order(hello: &ClientHelloPayload) -> Vec<ExtensionType> {
    if let Some(craft_extensions) = hello.craft_extensions.as_ref() {
        return craft_extensions
            .iter()
            .map(|extension| extension.extension_type())
            .collect();
    }

    hello.used_extensions_in_encoding_order()
}

fn outer_supported_versions_has_tls12(outer_hello: &ClientHelloPayload) -> bool {
    if let Some(craft_extensions) = outer_hello.craft_extensions.as_ref() {
        if let Some(payload) = craft_extensions
            .iter()
            .find(|ext| ext.extension_type() == ExtensionType::SupportedVersions)
            .and_then(|ext| ext.raw_payload())
        {
            return supported_versions_payload_has_tls12(payload);
        }
    }

    outer_hello
        .supported_versions
        .map(|versions| versions.tls12)
        .unwrap_or(false)
}

fn supported_versions_payload_has_tls12(payload: &[u8]) -> bool {
    let Some((&len, versions)) = payload.split_first() else {
        return false;
    };
    let len = usize::from(len);
    if len > versions.len() || len % 2 != 0 {
        return false;
    }

    versions[..len]
        .chunks_exact(2)
        .any(|version| {
            u16::from_be_bytes([version[0], version[1]]) == u16::from(ProtocolVersion::TLSv1_2)
        })
}

fn is_tls13_or_grease_cipher_suite(cipher_suite: CipherSuite) -> bool {
    let value = u16::from(cipher_suite);
    if value & 0x0f0f == 0x0a0a && value >> 8 == value & 0xff {
        return true;
    }

    matches!(
        cipher_suite,
        CipherSuite::TLS13_AES_128_GCM_SHA256
            | CipherSuite::TLS13_AES_256_GCM_SHA384
            | CipherSuite::TLS13_CHACHA20_POLY1305_SHA256
            | CipherSuite::TLS13_AES_128_CCM_SHA256
            | CipherSuite::TLS13_AES_128_CCM_8_SHA256
            | CipherSuite::TLS13_SM4_GCM_SM3
            | CipherSuite::TLS13_SM4_CCM_SM3
    )
}

/// The last eight bytes of the ServerHello's random, taken from a Handshake message containing it.
///
/// This has:
/// - a HandshakeType (1 byte),
/// - an exterior length (3 bytes),
/// - the legacy_version (2 bytes), and
/// - the balance of the random field (24 bytes).
const SERVER_HELLO_ECH_CONFIRMATION_SPAN: core::ops::Range<usize> =
    (1 + 3 + 2 + 24)..(1 + 3 + 2 + 32);

/// Returned from EchState::check_acceptance when the server has accepted the ECH offer.
///
/// Holds the state required to continue the handshake with the inner hello from the ECH offer.
pub(crate) struct EchAccepted {
    pub(crate) transcript: HandshakeHash,
    pub(crate) random: Random,
    pub(crate) sent_extensions: Vec<ExtensionType>,
}

#[cfg(test)]
mod tests {
    use std::string::String;

    use super::*;
    use crate::crypto::hpke::{HpkeAead, HpkeKdf};
    use crate::crypto::{CipherSuite, TEST_PROVIDER};
    use crate::msgs::{
        Compression, Random, ServerExtensions, SessionId, SupportedProtocolVersions,
    };

    #[test]
    fn server_hello_conf_alters_server_hello_random() {
        let server_hello = ServerHelloPayload {
            legacy_version: ProtocolVersion::TLSv1_2,
            random: Random([0xffu8; 32]),
            session_id: SessionId::empty(),
            cipher_suite: CipherSuite::TLS13_AES_256_GCM_SHA384,
            compression_method: Compression::Null,
            extensions: Box::new(ServerExtensions::default()),
        };
        let message = Message {
            version: ProtocolVersion::TLSv1_3,
            payload: MessagePayload::handshake(HandshakeMessagePayload(
                HandshakePayload::ServerHello(server_hello.clone()),
            )),
        };
        let Message {
            payload:
                MessagePayload::Handshake {
                    encoded: server_hello_encoded_before,
                    ..
                },
            ..
        } = &message
        else {
            unreachable!("ServerHello is a handshake message");
        };

        let message = EchState::server_hello_conf(&server_hello, server_hello_encoded_before);

        let Message {
            payload:
                MessagePayload::Handshake {
                    encoded: server_hello_encoded_after,
                    ..
                },
            ..
        } = &message
        else {
            unreachable!("ServerHello is a handshake message");
        };

        assert_eq!(
            std::format!("{server_hello_encoded_before:x?}"),
            "020000280303ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff001302000000",
            "beforehand eight bytes at end of Random should be 0xff here ^^^^^^^^^^^^^^^^            "
        );
        assert_eq!(
            std::format!("{server_hello_encoded_after:x?}"),
            "020000280303ffffffffffffffffffffffffffffffffffffffffffffffff0000000000000000001302000000",
            "                          afterwards those bytes are zeroed ^^^^^^^^^^^^^^^^            "
        );
    }

    #[test]
    fn inner_client_hello_length_conceals_inner_name_length() {
        let base_inner_len = inner_hello_encoding_for_name(dns_name_of_len(1), true).len();
        assert!(
            base_inner_len % 32 == 0,
            "inner hello length must be 32-byte padded"
        );
        assert!(
            base_inner_len >= 256,
            "inner hello must include inner name and its padding"
        );

        for inner_name_len in 1..251 {
            assert_eq!(
                inner_hello_encoding_for_name(dns_name_of_len(inner_name_len), true).len(),
                base_inner_len,
                "all inner hello lengths must be invariant wrt inner name length"
            );
        }
    }

    #[test]
    fn inner_client_hello_length_does_not_leak_length_of_omitted_inner_name() {
        let base_inner_len = inner_hello_encoding_for_name(dns_name_of_len(1), false).len();
        assert!(
            base_inner_len % 32 == 0,
            "inner hello length must be 32-byte padded"
        );
        assert!(
            base_inner_len >= 256,
            "inner hello must include maximum_name_length bytes of padding"
        );

        for inner_name_len in 1..251 {
            assert_eq!(
                inner_hello_encoding_for_name(dns_name_of_len(inner_name_len), false).len(),
                base_inner_len,
                "all inner hello lengths must be invariant wrt inner name length"
            );
        }
    }

    #[test]
    fn inner_client_hello_zero_max_name_length_uses_no_name_padding() {
        let name = DnsName::try_from("private.example").unwrap();
        let zero_max_len = inner_hello_encoding_for_name_with_max_len(name.clone(), true, 0).len();
        let full_max_len = inner_hello_encoding_for_name_with_max_len(name, true, 255).len();

        assert_eq!(zero_max_len % 32, 0);
        assert!(
            zero_max_len < full_max_len,
            "maximum_name_length=0 must not be treated as 255"
        );
    }

    #[test]
    fn inner_client_hello_nss_padding_matches_firefox_residue() {
        let name = DnsName::try_from("private.example").unwrap();
        let standard = inner_hello_encoding_for_name_with_padding_style(
            name.clone(),
            true,
            0,
            EchPaddingStyle::Standard,
        );
        let nss =
            inner_hello_encoding_for_name_with_padding_style(name, true, 0, EchPaddingStyle::Nss);

        assert_eq!(standard.len() % 32, 0);
        assert_eq!(nss.len() % 32, 31);
        assert_eq!((standard.len() + 16) % 32, 16);
        assert_eq!((nss.len() + 16) % 32, 15);
    }

    #[test]
    fn inner_client_hello_omits_tls12_when_outer_allows_tls12() {
        let encoded = inner_hello_encoding_for_outer_versions(SupportedProtocolVersions {
            tls13: true,
            tls12: true,
        });
        let versions = supported_versions_from_client_hello_payload(&encoded);

        assert!(versions.tls13);
        assert!(!versions.tls12);
    }

    fn supported_versions_from_client_hello_payload(wire: &[u8]) -> SupportedProtocolVersions {
        let mut offset = 0;
        take(wire, &mut offset, 2); // legacy_version
        take(wire, &mut offset, 32); // random
        let session_id_len = take_u8(wire, &mut offset) as usize;
        take(wire, &mut offset, session_id_len);
        let cipher_suites_len = take_u16(wire, &mut offset) as usize;
        take(wire, &mut offset, cipher_suites_len);
        let compression_methods_len = take_u8(wire, &mut offset) as usize;
        take(wire, &mut offset, compression_methods_len);

        let extensions_len = take_u16(wire, &mut offset) as usize;
        let extensions_end = offset + extensions_len;
        while offset < extensions_end {
            let typ = ExtensionType(take_u16(wire, &mut offset));
            let len = take_u16(wire, &mut offset) as usize;
            let payload = take(wire, &mut offset, len);
            if typ == ExtensionType::SupportedVersions {
                return SupportedProtocolVersions::read(&mut Reader::new(payload)).unwrap();
            }
        }

        panic!("missing supported_versions extension");
    }

    fn take_u8(bytes: &[u8], offset: &mut usize) -> u8 {
        let value = bytes[*offset];
        *offset += 1;
        value
    }

    fn take_u16(bytes: &[u8], offset: &mut usize) -> u16 {
        let value = u16::from_be_bytes([bytes[*offset], bytes[*offset + 1]]);
        *offset += 2;
        value
    }

    fn take<'a>(bytes: &'a [u8], offset: &mut usize, len: usize) -> &'a [u8] {
        let start = *offset;
        *offset += len;
        &bytes[start..*offset]
    }

    fn inner_hello_encoding_for_outer_versions(versions: SupportedProtocolVersions) -> Vec<u8> {
        let name = DnsName::try_from("private.example").unwrap();
        inner_hello_encoding_for_extensions(
            name.clone(),
            true,
            255,
            ClientExtensions {
                server_name: Some(ServerNamePayload::from(&name)),
                supported_versions: Some(versions),
                ..Default::default()
            },
            EchPaddingStyle::Standard,
        )
    }

    fn inner_hello_encoding_for_name(name: DnsName<'static>, enable_sni: bool) -> Vec<u8> {
        inner_hello_encoding_for_name_with_max_len(name, enable_sni, 255)
    }

    fn inner_hello_encoding_for_name_with_max_len(
        name: DnsName<'static>,
        enable_sni: bool,
        maximum_name_length: u8,
    ) -> Vec<u8> {
        inner_hello_encoding_for_name_with_padding_style(
            name,
            enable_sni,
            maximum_name_length,
            EchPaddingStyle::Standard,
        )
    }

    fn inner_hello_encoding_for_name_with_padding_style(
        name: DnsName<'static>,
        enable_sni: bool,
        maximum_name_length: u8,
        padding_style: EchPaddingStyle,
    ) -> Vec<u8> {
        inner_hello_encoding_for_extensions(
            name.clone(),
            enable_sni,
            maximum_name_length,
            ClientExtensions {
                server_name: Some(ServerNamePayload::from(&name)),
                ..Default::default()
            },
            padding_style,
        )
    }

    fn inner_hello_encoding_for_extensions(
        name: DnsName<'static>,
        enable_sni: bool,
        maximum_name_length: u8,
        extensions: ClientExtensions<'static>,
        padding_style: EchPaddingStyle,
    ) -> Vec<u8> {
        let config = EchConfig {
            config: EchConfigPayload::V18(EchConfigContents {
                key_config: HpkeKeyConfig {
                    config_id: 0,
                    kem_id: MockHpke::SUITE.kem,
                    public_key: vec![0; 32].into(),
                    symmetric_cipher_suites: vec![],
                },
                maximum_name_length,
                public_name: DnsName::try_from("public").unwrap(),
                extensions: vec![],
            }),
            suite: &MockHpke,
            tls13_only: true,
        };

        EchState::new(
            &config,
            ServerName::from(name.clone()),
            Protocol::Tcp,
            false,
            TEST_PROVIDER.secure_random,
            enable_sni,
        )
        .unwrap()
        .encode_inner_hello(
            &ClientHelloPayload {
                client_version: ProtocolVersion::TLSv1_3,
                random: Random([0u8; 32]),
                session_id: SessionId::empty(),
                cipher_suites: vec![CipherSuite::TLS13_AES_128_GCM_SHA256],
                compression_methods: vec![Compression::Null],
                extensions: Box::new(extensions),
                craft_extensions: None,
            },
            None,
            None,
            padding_style,
        )
    }

    fn dns_name_of_len(mut len: usize) -> DnsName<'static> {
        let mut s = String::new();
        let labels = len.div_ceil(63);
        for _ in 0..labels {
            let chars = Ord::min(len, 63);
            len -= chars;
            for _ in 0..chars {
                s.push('a');
            }
            if len != 0 {
                s.push('.');
            }
        }
        DnsName::try_from(s).unwrap()
    }

    #[derive(Debug)]
    struct MockHpke;

    impl MockHpke {
        const SUITE: HpkeSuite = HpkeSuite {
            kem: HpkeKem::DHKEM_P256_HKDF_SHA256,
            sym: HpkeSymmetricCipherSuite {
                kdf_id: HpkeKdf::HKDF_SHA256,
                aead_id: HpkeAead::AES_128_GCM,
            },
        };
    }

    impl Hpke for MockHpke {
        #[cfg_attr(coverage_nightly, coverage(off))]
        fn seal(
            &self,
            _info: &[u8],
            _aad: &[u8],
            _plaintext: &[u8],
            _pub_key: &HpkePublicKey,
        ) -> Result<(EncapsulatedSecret, Vec<u8>), Error> {
            todo!()
        }

        fn setup_sealer(
            &self,
            _info: &[u8],
            _pub_key: &HpkePublicKey,
        ) -> Result<(EncapsulatedSecret, Box<dyn HpkeSealer + 'static>), Error> {
            Ok((EncapsulatedSecret(vec![]), Box::new(MockHpkeSealer)))
        }

        #[cfg_attr(coverage_nightly, coverage(off))]
        fn open(
            &self,
            _enc: &EncapsulatedSecret,
            _info: &[u8],
            _aad: &[u8],
            _ciphertext: &[u8],
            _secret_key: &crate::crypto::hpke::HpkePrivateKey,
        ) -> Result<Vec<u8>, Error> {
            todo!()
        }

        #[cfg_attr(coverage_nightly, coverage(off))]
        fn setup_opener(
            &self,
            _enc: &EncapsulatedSecret,
            _info: &[u8],
            _secret_key: &crate::crypto::hpke::HpkePrivateKey,
        ) -> Result<Box<dyn crate::crypto::hpke::HpkeOpener + 'static>, Error> {
            todo!()
        }

        #[cfg_attr(coverage_nightly, coverage(off))]
        fn generate_key_pair(
            &self,
        ) -> Result<(HpkePublicKey, crate::crypto::hpke::HpkePrivateKey), Error> {
            todo!()
        }

        fn suite(&self) -> HpkeSuite {
            Self::SUITE
        }
    }

    #[derive(Debug)]
    struct MockHpkeSealer;

    impl HpkeSealer for MockHpkeSealer {
        #[cfg_attr(coverage_nightly, coverage(off))]
        fn seal(&mut self, _aad: &[u8], _plaintext: &[u8]) -> Result<Vec<u8>, Error> {
            todo!()
        }
    }
}
