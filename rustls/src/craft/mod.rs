#![allow(missing_docs)]

mod fingerprints;
pub use fingerprints::*;

use alloc::borrow::Cow;
use alloc::boxed::Box;
use alloc::format;
use alloc::string::String;
use alloc::vec;
use alloc::vec::Vec;
use core::fmt::{self, Debug};
use std::collections::HashMap;

use num_bigint::BigUint;

use crate::ClientConfig;
use crate::compress;
use crate::crypto::cipher::Payload;
use crate::crypto::hpke::HpkeAead;
use crate::crypto::kx::ffdhe::{self, FfdheGroup};
use crate::crypto::kx::{
    ActiveKeyExchange, NamedGroup, SharedSecret, StartedKeyExchange, SupportedKxGroup,
};
use crate::crypto::tls13::OkmBlock;
use crate::crypto::{CipherSuite, HashAlgorithm, SecureRandom, SignatureScheme};
use crate::enums::{
    ApplicationProtocol, CertificateCompressionAlgorithm, CertificateType, ProtocolVersion,
};
use crate::error::{Error, PeerMisbehaved};
use crate::msgs::{
    ClientExtensions, ClientHelloPayload, ExtensionType, HandshakeMessagePayload, HandshakePayload,
    HelloRetryRequest, KeyShareEntry, PskKeyExchangeModes,
};
use crate::msgs::{Codec, LengthPrefixedBuffer, ListLength, TlsListElement};
use crate::sync::Arc;

#[derive(Clone, Debug, Default)]
pub(crate) struct CraftOptions(Option<FingerprintBuilder>);

impl CraftOptions {
    pub(crate) fn is_enabled(&self) -> bool {
        self.0.is_some()
    }

    fn get(&self) -> Option<&FingerprintBuilder> {
        self.0.as_ref()
    }

    pub(crate) fn patch_client_hello(
        &self,
        data: &mut CraftConnectionData,
        config: &ClientConfig,
        hrr: Option<&HelloRetryRequest>,
        hello: &mut ClientHelloPayload,
    ) -> CraftPatchResult {
        let Some(builder) = self.get() else {
            return CraftPatchResult::default();
        };
        if let Some(error) = &builder.validation_error {
            return CraftPatchResult::fatal(Error::General(error.clone()));
        }

        let result = builder
            .fingerprint
            .patch_client_hello(data, config, hrr, hello);
        if result.error.is_some() {
            return result;
        }

        if builder.override_suite {
            builder
                .fingerprint
                .patch_cipher(data, &mut hello.cipher_suites);
        }

        result
    }
}

#[derive(Default)]
pub(crate) struct CraftPatchResult {
    pub(crate) key_shares: Vec<(&'static dyn SupportedKxGroup, StartedKeyExchange)>,
    pub(crate) error: Option<Error>,
}

impl CraftPatchResult {
    fn fatal(error: Error) -> Self {
        Self {
            key_shares: Vec::new(),
            error: Some(error),
        }
    }
}

enum CraftExtensionError {
    Skip,
    Fatal(Error),
}

impl From<()> for CraftExtensionError {
    fn from((): ()) -> Self {
        Self::Skip
    }
}

pub(crate) fn refresh_psk_binder(hmp: &mut HandshakeMessagePayload<'_>) {
    let HandshakePayload::ClientHello(hello) = &mut hmp.0 else {
        return;
    };
    if hello.preshared_key_offer.is_none() {
        return;
    }

    let Some(psk_extension) =
        encode_single_extension(&hello.extensions, ExtensionType::PreSharedKey)
    else {
        return;
    };
    let Some(craft_extensions) = &mut hello.craft_extensions else {
        return;
    };
    let Some(craft_psk_extension) = craft_extensions
        .iter_mut()
        .find(|extension| extension.typ == ExtensionType::PreSharedKey)
    else {
        return;
    };

    *craft_psk_extension = psk_extension;
}

#[allow(dead_code)]
#[repr(usize)]
enum BoringSslGreaseIndex {
    Cipher,
    Group,
    Extension1,
    Extension2,
    Version,
    SignatureAlgorithm,
    Alpn,
    PskKeyExchangeMode,
    TicketExtension,
    EchConfigId,
    NumOfGrease,
}

#[derive(Debug)]
struct GreaseSeed([u16; BoringSslGreaseIndex::NumOfGrease as usize]);

impl GreaseSeed {
    fn get(&self, idx: BoringSslGreaseIndex) -> u16 {
        self.0[idx as usize]
    }

    fn get_psk_key_exchange_mode(&self) -> u8 {
        let random = (self.get(BoringSslGreaseIndex::PskKeyExchangeMode) >> 8) as u8;
        0x0b + ((random >> 5) * 0x1f)
    }
}

#[derive(Debug)]
pub(crate) struct CraftConnectionData {
    grease_seed: GreaseSeed,
    extension_order: Vec<usize>,
}

impl CraftConnectionData {
    pub(crate) fn new(secure_random: &dyn SecureRandom) -> Result<Self, Error> {
        use BoringSslGreaseIndex::*;

        let mut grease_seed = [0u16; NumOfGrease as usize];
        for seed in grease_seed.iter_mut() {
            let mut random = [0u8; 2];
            secure_random.fill(&mut random)?;
            let random = u16::from_be_bytes(random);
            let unit = (random & 0xf0u16) | 0x0au16;
            *seed = unit << 8 | unit;
        }
        if grease_seed[Extension1 as usize] == grease_seed[Extension2 as usize] {
            grease_seed[Extension2 as usize] ^= 0x1010;
        }

        Ok(Self {
            grease_seed: GreaseSeed(grease_seed),
            extension_order: Vec::new(),
        })
    }
}

#[derive(Debug)]
pub enum GreaseOr<T> {
    Grease,
    T(T),
}

impl<T: Clone> Clone for GreaseOr<T> {
    fn clone(&self) -> Self {
        match self {
            Self::Grease => Self::Grease,
            Self::T(t) => Self::T(t.clone()),
        }
    }
}

#[allow(dead_code)]
impl<T: Clone> GreaseOr<T> {
    pub(crate) fn is_grease(&self) -> bool {
        matches!(self, Self::Grease)
    }

    pub(crate) fn val(&self) -> T {
        match self {
            Self::Grease => panic!("GREASE value does not have a fixed value"),
            Self::T(t) => t.clone(),
        }
    }
}

pub use GreaseOr::Grease;

pub(crate) trait CreateUnknown: Clone + Debug {
    fn create_unknown(grease: u16) -> Self;
}

impl<T> GreaseOr<T> {
    fn val_or(&self, grease: u16) -> T
    where
        T: CreateUnknown,
    {
        match self {
            Self::Grease => T::create_unknown(grease),
            Self::T(t) => t.clone(),
        }
    }
}

impl<T> From<T> for GreaseOr<T> {
    fn from(value: T) -> Self {
        Self::T(value)
    }
}

pub type GreaseOrCurve = GreaseOr<NamedGroup>;
pub type GreaseOrVersion = GreaseOr<ProtocolVersion>;
pub type GreaseOrCipher = GreaseOr<CipherSuite>;
pub type GreaseOrSignatureScheme = GreaseOr<SignatureScheme>;

impl CreateUnknown for NamedGroup {
    fn create_unknown(grease: u16) -> Self {
        Self(grease)
    }
}

impl CreateUnknown for ProtocolVersion {
    fn create_unknown(grease: u16) -> Self {
        Self(grease)
    }
}

impl CreateUnknown for CipherSuite {
    fn create_unknown(grease: u16) -> Self {
        Self(grease)
    }
}

impl CreateUnknown for SignatureScheme {
    fn create_unknown(grease: u16) -> Self {
        Self(grease)
    }
}

#[derive(Debug, Clone)]
pub enum GreaseOrProtocol {
    Grease,
    Protocol(&'static [u8]),
}

impl From<&'static [u8]> for GreaseOrProtocol {
    fn from(value: &'static [u8]) -> Self {
        Self::Protocol(value)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum GreaseOrPskKeyExchangeMode {
    Grease,
    T(PSKKeyExchangeMode),
}

impl GreaseOrPskKeyExchangeMode {
    fn val_or(&self, grease: u8) -> PSKKeyExchangeMode {
        match self {
            Self::Grease => PSKKeyExchangeMode(grease),
            Self::T(mode) => *mode,
        }
    }
}

impl From<PSKKeyExchangeMode> for GreaseOrPskKeyExchangeMode {
    fn from(value: PSKKeyExchangeMode) -> Self {
        Self::T(value)
    }
}

#[derive(Debug, Clone, Copy)]
pub enum ApplicationSettingsCodepoint {
    New,
    Old,
    Custom(ExtensionType),
}

impl ApplicationSettingsCodepoint {
    fn extension_type(self) -> ExtensionType {
        match self {
            Self::New => ExtensionType::ApplicationSettings,
            Self::Old => ExtensionType::ApplicationSettingsOld,
            Self::Custom(typ) => typ,
        }
    }
}

#[derive(Debug)]
struct CraftFfdheKxGroup {
    name: NamedGroup,
    group: FfdheGroup<'static>,
    secure_random: &'static dyn SecureRandom,
}

impl SupportedKxGroup for CraftFfdheKxGroup {
    fn start(&self) -> Result<StartedKeyExchange, Error> {
        let mut exponent = vec![0; ffdhe_exponent_len(self.group)];
        self.secure_random.fill(&mut exponent)?;
        if exponent.iter().all(|byte| *byte == 0) {
            exponent[0] = 1;
        }

        let x = BigUint::from_bytes_be(&exponent);
        let p = BigUint::from_bytes_be(self.group.p);
        let g = BigUint::from_bytes_be(self.group.g);
        let x_pub = to_bytes_be_with_len(g.modpow(&x, &p), self.group.p.len());

        Ok(StartedKeyExchange::Single(Box::new(ActiveCraftFfdheKx {
            x_pub,
            x,
            p,
            group: self.group,
            named_group: self.name,
        })))
    }

    fn ffdhe_group(&self) -> Option<FfdheGroup<'static>> {
        Some(self.group)
    }

    fn name(&self) -> NamedGroup {
        self.name
    }
}

struct ActiveCraftFfdheKx {
    x_pub: Vec<u8>,
    x: BigUint,
    p: BigUint,
    group: FfdheGroup<'static>,
    named_group: NamedGroup,
}

impl ActiveKeyExchange for ActiveCraftFfdheKx {
    fn complete(self: Box<Self>, peer_pub_key: &[u8]) -> Result<SharedSecret, Error> {
        let peer_pub = BigUint::from_bytes_be(peer_pub_key);
        let min_peer_pub = BigUint::from(2u8);
        let max_peer_pub = &self.p - BigUint::from(2u8);
        if peer_pub < min_peer_pub || peer_pub > max_peer_pub {
            return Err(PeerMisbehaved::InvalidKeyShare.into());
        }

        let secret = peer_pub.modpow(&self.x, &self.p);
        let secret = to_bytes_be_with_len(secret, self.group.p.len());

        Ok(SharedSecret::from(&secret[..]))
    }

    fn pub_key(&self) -> &[u8] {
        &self.x_pub
    }

    fn ffdhe_group(&self) -> Option<FfdheGroup<'static>> {
        Some(self.group)
    }

    fn group(&self) -> NamedGroup {
        self.named_group
    }
}

fn ffdhe_exponent_len(group: FfdheGroup<'static>) -> usize {
    match group.p.len() {
        0..=256 => 32,
        257..=384 => 40,
        385..=512 => 48,
        513..=768 => 56,
        _ => 64,
    }
}

fn to_bytes_be_with_len(n: BigUint, len_bytes: usize) -> Vec<u8> {
    let mut bytes = n.to_bytes_le();
    bytes.resize(len_bytes, 0);
    bytes.reverse();
    bytes
}

fn craft_ffdhe_group(
    name: NamedGroup,
    group: FfdheGroup<'static>,
    secure_random: &'static dyn SecureRandom,
) -> &'static dyn SupportedKxGroup {
    Box::leak(Box::new(CraftFfdheKxGroup {
        name,
        group,
        secure_random,
    }))
}

pub(crate) fn to_missing_kx_group(
    group: &NamedGroup,
    secure_random: &'static dyn SecureRandom,
) -> Option<&'static dyn SupportedKxGroup> {
    match *group {
        NamedGroup::FFDHE2048 => Some(craft_ffdhe_group(*group, ffdhe::FFDHE2048, secure_random)),
        NamedGroup::FFDHE3072 => Some(craft_ffdhe_group(*group, ffdhe::FFDHE3072, secure_random)),
        NamedGroup::FFDHE4096 => Some(craft_ffdhe_group(*group, ffdhe::FFDHE4096, secure_random)),
        NamedGroup::FFDHE6144 => Some(craft_ffdhe_group(*group, ffdhe::FFDHE6144, secure_random)),
        NamedGroup::FFDHE8192 => Some(craft_ffdhe_group(*group, ffdhe::FFDHE8192, secure_random)),
        _ => None,
    }
}

#[derive(Debug, Clone, Copy)]
#[non_exhaustive]
pub enum EchPlaceholderAead {
    Fixed(HpkeAead),
    NssGrease,
}

#[derive(Debug, Clone)]
pub enum CraftExtension {
    Grease1,
    Grease2,
    RenegotiationInfo,
    Raw(ExtensionType, &'static [u8]),
    EchPlaceholder {
        payload_body_len: usize,
        random_config_id: bool,
        aead: EchPlaceholderAead,
    },
    BoringSslEchGrease {
        aead: HpkeAead,
    },
    SupportedCurves(&'static [GreaseOrCurve]),
    SupportedVersions(&'static [GreaseOrVersion]),
    SignatureAlgorithms(&'static [GreaseOrSignatureScheme]),
    SignatureAlgorithmsCert(&'static [GreaseOrSignatureScheme]),
    SignedCertificateTimestamp,
    KeyShare(&'static [GreaseOrCurve]),
    NextProtocolNegotiation,
    ChannelId,
    UseSrtp {
        profiles: &'static [u16],
        mki: &'static [u8],
    },
    FakeApplicationSettings,
    ApplicationSettings {
        codepoint: ApplicationSettingsCodepoint,
        protocols: &'static [&'static [u8]],
    },
    FakeCompressCert,
    CompressCert(&'static [CertificateCompressionAlgorithm]),
    Padding,
    Protocols(&'static [&'static [u8]]),
    ProtocolsWithGrease(&'static [GreaseOrProtocol]),
    DelegatedCredentials(&'static [GreaseOrSignatureScheme]),
    FakeDelegatedCredentials(&'static [SignatureScheme]),
    RecordSizeLimit(u16),
    FakeRecordSizeLimit(u16),
    PresharedKeyModes(&'static [GreaseOrPskKeyExchangeMode]),
    PostHandshakeAuth,
    ClientCertificateTypes(&'static [CertificateType]),
    ServerCertificateTypes(&'static [CertificateType]),
    CertificateAuthorities(&'static [&'static [u8]]),
    QuicTransportParameters(&'static [u8]),
    QuicTransportParametersLegacy(&'static [u8]),
    TrustAnchors(&'static [&'static [u8]]),
    Pake(&'static [u8]),
}

fn encode_protocol_list(protocols: &[&[u8]]) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let list = LengthPrefixedBuffer::new(ListLength::U16, &mut payload);
        for protocol in protocols {
            append_opaque_u8(list.buf, protocol);
        }
    }
    payload
}

fn encode_protocol_list_with_grease(
    protocols: &[GreaseOrProtocol],
    grease_protocol: u16,
) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let list = LengthPrefixedBuffer::new(ListLength::U16, &mut payload);
        for protocol in protocols {
            match protocol {
                GreaseOrProtocol::Grease => {
                    2u8.encode(list.buf);
                    grease_protocol.encode(list.buf);
                }
                GreaseOrProtocol::Protocol(protocol) => append_opaque_u8(list.buf, protocol),
            }
        }
    }
    payload
}

fn encode_application_settings(protocols: &[&[u8]]) -> Vec<u8> {
    encode_protocol_list(protocols)
}

fn encode_certificate_compression_algorithms(
    algorithms: &[CertificateCompressionAlgorithm],
) -> Vec<u8> {
    let mut payload = Vec::new();
    let byte_len = algorithms
        .len()
        .checked_mul(2)
        .and_then(|len| u8::try_from(len).ok())
        .expect("certificate compression algorithm list too long");
    byte_len.encode(&mut payload);
    for algorithm in algorithms {
        algorithm.encode(&mut payload);
    }
    payload
}

fn encode_ech_placeholder(
    config: &ClientConfig,
    payload_body_len: usize,
    random_config_id: bool,
    aead: EchPlaceholderAead,
) -> Result<Vec<u8>, Error> {
    let payload_body_len = u16::try_from(payload_body_len)
        .map_err(|_| Error::General("ECH placeholder payload body is too large".into()))?;

    let mut payload = Vec::with_capacity(42 + usize::from(payload_body_len));
    payload.push(0x00); // ECHClientHelloOuter
    payload.extend_from_slice(&0x0001u16.to_be_bytes()); // HKDF-SHA256
    let aead_pos = payload.len();
    payload.extend_from_slice(&0u16.to_be_bytes());
    let config_id_pos = payload.len();
    payload.push(0);
    payload.extend_from_slice(&0x0020u16.to_be_bytes()); // enc length

    let enc_start = payload.len();
    payload.resize(enc_start + 32, 0);
    payload.extend_from_slice(&payload_body_len.to_be_bytes());
    let body_start = payload.len();
    payload.resize(body_start + usize::from(payload_body_len), 0);

    match aead {
        EchPlaceholderAead::Fixed(aead) => {
            payload[aead_pos..aead_pos + 2].copy_from_slice(&aead.0.to_be_bytes());
            if random_config_id {
                config
                    .provider()
                    .secure_random
                    .fill(&mut payload[config_id_pos..config_id_pos + 1])?;
            }
            config
                .provider()
                .secure_random
                .fill(&mut payload[enc_start..enc_start + 32])?;
            config
                .provider()
                .secure_random
                .fill(&mut payload[body_start..])?;
        }
        EchPlaceholderAead::NssGrease => {
            let raw_data = nss_grease_ech_raw_data(config, 34 + usize::from(payload_body_len))?;
            let aead = if raw_data[0] & 1 == 1 {
                HpkeAead::AES_128_GCM
            } else {
                HpkeAead::CHACHA20_POLY_1305
            };
            payload[aead_pos..aead_pos + 2].copy_from_slice(&aead.0.to_be_bytes());
            if random_config_id {
                payload[config_id_pos] = raw_data[1];
            }
            payload[enc_start..enc_start + 32].copy_from_slice(&raw_data[2..34]);
            payload[body_start..].copy_from_slice(&raw_data[34..]);
        }
    }

    Ok(payload)
}

fn random_size(config: &ClientConfig, min: usize, max: usize) -> Result<usize, Error> {
    debug_assert!(min < max);
    let mut random = [0; 8];
    config
        .provider()
        .secure_random
        .fill(&mut random)?;
    let value = u64::from_ne_bytes(random) as usize;
    Ok(value % (max - min + 1) + min)
}

fn boring_ssl_ech_grease_payload_body_len(
    config: &ClientConfig,
    aead: HpkeAead,
) -> Result<usize, Error> {
    let tag_len = aead
        .tag_len()
        .ok_or_else(|| Error::General(format!("unsupported ECH GREASE AEAD {aead:?}")))?;
    Ok(32 * random_size(config, 128 / 32, 224 / 32)? + tag_len)
}

fn nss_grease_ech_raw_data(config: &ClientConfig, len: usize) -> Result<Vec<u8>, Error> {
    let mut prk = [0; 32];
    config
        .provider()
        .secure_random
        .fill(&mut prk)?;

    let Some(suite) = config
        .provider()
        .tls13_cipher_suites
        .iter()
        .find(|suite| suite.common.hash_provider.algorithm() == HashAlgorithm::SHA256)
    else {
        return Err(Error::General(
            "craft fingerprint requires HKDF-SHA256 support for NSS GREASE ECH".into(),
        ));
    };

    let expander = suite
        .hkdf_provider
        .expander_for_okm(&OkmBlock::new(&prk));
    let mut raw_data = vec![0; len];
    expander
        .expand_slice(&[], &mut raw_data)
        .map_err(|_| Error::General("NSS GREASE ECH placeholder is too large".into()))?;
    Ok(raw_data)
}

fn encode_signature_scheme_list(schemes: &[GreaseOrSignatureScheme], grease: u16) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let list = LengthPrefixedBuffer::new(ListLength::U16, &mut payload);
        for scheme in schemes {
            scheme.val_or(grease).encode(list.buf);
        }
    }
    payload
}

fn encode_psk_key_exchange_modes(modes: &[GreaseOrPskKeyExchangeMode], grease: u8) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let list = LengthPrefixedBuffer::new(
            ListLength::NonZeroU8 {
                empty_error: crate::error::InvalidMessage::IllegalEmptyList("PskKeyExchangeModes"),
            },
            &mut payload,
        );
        for mode in modes {
            mode.val_or(grease).encode(list.buf);
        }
    }
    payload
}

fn encode_certificate_types(types: &[CertificateType]) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let list = LengthPrefixedBuffer::new(
            ListLength::NonZeroU8 {
                empty_error: crate::error::InvalidMessage::IllegalEmptyList("CertificateTypes"),
            },
            &mut payload,
        );
        for typ in types {
            typ.encode(list.buf);
        }
    }
    payload
}

fn encode_use_srtp(profiles: &[u16], mki: &[u8]) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let profile_ids = LengthPrefixedBuffer::new(ListLength::U16, &mut payload);
        for profile in profiles {
            profile.encode(profile_ids.buf);
        }
    }
    append_opaque_u8(&mut payload, mki);
    payload
}

fn encode_certificate_authorities(authorities: &[&[u8]]) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let list = LengthPrefixedBuffer::new(ListLength::U16, &mut payload);
        for authority in authorities {
            append_opaque_u16(list.buf, authority);
        }
    }
    payload
}

fn encode_trust_anchors(anchors: &[&[u8]]) -> Vec<u8> {
    let mut payload = Vec::new();
    {
        let list = LengthPrefixedBuffer::new(ListLength::U16, &mut payload);
        for anchor in anchors {
            append_opaque_u8(list.buf, anchor);
        }
    }
    payload
}

fn append_opaque_u8(out: &mut Vec<u8>, payload: &[u8]) {
    let len = u8::try_from(payload.len()).expect("u8 length-prefixed payload too large");
    len.encode(out);
    out.extend_from_slice(payload);
}

fn append_opaque_u16(out: &mut Vec<u8>, payload: &[u8]) {
    let len = u16::try_from(payload.len()).expect("u16 length-prefixed payload too large");
    len.encode(out);
    out.extend_from_slice(payload);
}

impl CraftExtension {
    fn to_wire_extension(
        &self,
        data: &mut CraftConnectionData,
        config: &ClientConfig,
        store: &mut HashMap<ExtensionType, CraftClientExtension>,
        hello: &ClientHelloPayload,
        hrr: Option<&HelloRetryRequest>,
    ) -> Result<(CraftClientExtension, CraftPatchResult), CraftExtensionError> {
        let craft_config = config.craft.get().ok_or(())?;
        let mut result = CraftPatchResult::default();

        let ext = match self {
            Self::Grease1 => CraftClientExtension::raw(
                ExtensionType(
                    data.grease_seed
                        .get(BoringSslGreaseIndex::Extension1),
                ),
                Vec::new(),
            ),
            Self::Grease2 => CraftClientExtension::raw(
                ExtensionType(
                    data.grease_seed
                        .get(BoringSslGreaseIndex::Extension2),
                ),
                vec![0],
            ),
            Self::RenegotiationInfo => {
                CraftClientExtension::raw(ExtensionType::RenegotiationInfo, vec![0])
            }
            Self::Raw(typ, payload) => CraftClientExtension::raw(*typ, payload.to_vec()),
            Self::EchPlaceholder {
                payload_body_len,
                random_config_id,
                aead,
            } => CraftClientExtension::raw(
                ExtensionType::EncryptedClientHello,
                encode_ech_placeholder(config, *payload_body_len, *random_config_id, *aead)
                    .map_err(CraftExtensionError::Fatal)?,
            ),
            Self::BoringSslEchGrease { aead } => {
                let payload_body_len = boring_ssl_ech_grease_payload_body_len(config, *aead)
                    .map_err(CraftExtensionError::Fatal)?;
                CraftClientExtension::raw(
                    ExtensionType::EncryptedClientHello,
                    encode_ech_placeholder(
                        config,
                        payload_body_len,
                        true,
                        EchPlaceholderAead::Fixed(*aead),
                    )
                    .map_err(CraftExtensionError::Fatal)?,
                )
            }
            Self::SupportedCurves(curves) => {
                if !craft_config.override_supported_curves {
                    store
                        .remove(&ExtensionType::EllipticCurves)
                        .ok_or(())?
                } else {
                    let groups = curves
                        .iter()
                        .map(|group| {
                            group.val_or(
                                data.grease_seed
                                    .get(BoringSslGreaseIndex::Group),
                            )
                        })
                        .collect::<Vec<_>>();
                    CraftClientExtension::encoded(ExtensionType::EllipticCurves, &groups)
                }
            }
            Self::SupportedVersions(versions) => {
                if !craft_config.override_version {
                    store
                        .remove(&ExtensionType::SupportedVersions)
                        .ok_or(())?
                } else {
                    let mut payload = Vec::new();
                    {
                        let inner = LengthPrefixedBuffer::new(
                            ListLength::NonZeroU8 {
                                empty_error: crate::error::InvalidMessage::IllegalEmptyList(
                                    "ProtocolVersions",
                                ),
                            },
                            &mut payload,
                        );
                        for version in *versions {
                            version
                                .val_or(
                                    data.grease_seed
                                        .get(BoringSslGreaseIndex::Version),
                                )
                                .encode(inner.buf);
                        }
                    }
                    CraftClientExtension::raw(ExtensionType::SupportedVersions, payload)
                }
            }
            Self::SignatureAlgorithms(schemes) => CraftClientExtension::raw(
                ExtensionType::SignatureAlgorithms,
                encode_signature_scheme_list(
                    schemes,
                    data.grease_seed
                        .get(BoringSslGreaseIndex::SignatureAlgorithm),
                ),
            ),
            Self::SignatureAlgorithmsCert(schemes) => CraftClientExtension::raw(
                ExtensionType::SignatureAlgorithmsCert,
                encode_signature_scheme_list(
                    schemes,
                    data.grease_seed
                        .get(BoringSslGreaseIndex::SignatureAlgorithm),
                ),
            ),
            Self::SignedCertificateTimestamp => {
                CraftClientExtension::raw(ExtensionType::SCT, Vec::new())
            }
            Self::KeyShare(key_share_spec) => {
                if hrr
                    .and_then(|hrr| hrr.key_share)
                    .is_some()
                    || !craft_config.override_keyshare
                {
                    store
                        .remove(&ExtensionType::KeyShare)
                        .ok_or(())?
                } else {
                    let mut shares = Vec::new();
                    let mut tracked_shares = Vec::new();
                    let mut component_shares: Vec<(NamedGroup, Vec<u8>)> = Vec::new();

                    for group_spec in *key_share_spec {
                        match group_spec {
                            Grease => shares.push(KeyShareEntry::new(
                                group_spec.val_or(
                                    data.grease_seed
                                        .get(BoringSslGreaseIndex::Group),
                                ),
                                vec![0],
                            )),
                            GreaseOr::T(group) => {
                                if shares
                                    .iter()
                                    .any(|share| share.group == *group)
                                {
                                    continue;
                                }
                                if let Some((_, component_share)) = component_shares
                                    .iter()
                                    .find(|(component_group, _)| component_group == group)
                                {
                                    shares
                                        .push(KeyShareEntry::new(*group, component_share.clone()));
                                    continue;
                                }

                                let Some(group_impl) = config
                                    .provider()
                                    .find_kx_group(*group, ProtocolVersion::TLSv1_3)
                                else {
                                    if craft_config.strict_mode {
                                        return Err(CraftExtensionError::Fatal(Error::General(
                                            format!(
                                                "craft fingerprint requires unsupported key share group {group:?}"
                                            ),
                                        )));
                                    }
                                    continue;
                                };

                                let Ok(started) = group_impl.start() else {
                                    if craft_config.strict_mode {
                                        return Err(CraftExtensionError::Fatal(Error::General(
                                            format!(
                                                "craft fingerprint failed to start key exchange for group {group:?}"
                                            ),
                                        )));
                                    }
                                    continue;
                                };
                                if let Some((hybrid, _)) = started.as_hybrid_checked(
                                    &config.provider().kx_groups,
                                    ProtocolVersion::TLSv1_3,
                                ) {
                                    let (component_group, component_share) = hybrid.component();
                                    component_shares
                                        .push((component_group, component_share.to_vec()));
                                }
                                shares.push(KeyShareEntry::new(*group, started.pub_key()));
                                tracked_shares.push((group_impl, started));
                            }
                        }
                    }

                    if shares.is_empty() {
                        return Err(CraftExtensionError::Skip);
                    }
                    result.key_shares = tracked_shares;
                    CraftClientExtension::encoded(ExtensionType::KeyShare, &shares)
                }
            }
            Self::NextProtocolNegotiation => {
                CraftClientExtension::raw(ExtensionType::NextProtocolNegotiation, Vec::new())
            }
            Self::ChannelId => CraftClientExtension::raw(ExtensionType::ChannelId, Vec::new()),
            Self::UseSrtp { profiles, mki } => {
                CraftClientExtension::raw(ExtensionType::UseSRTP, encode_use_srtp(profiles, mki))
            }
            Self::FakeApplicationSettings => CraftClientExtension::raw(
                ExtensionType::ApplicationSettingsOld,
                encode_application_settings(&[b"h2"]),
            ),
            Self::ApplicationSettings {
                codepoint,
                protocols,
            } => CraftClientExtension::raw(
                codepoint.extension_type(),
                encode_application_settings(protocols),
            ),
            Self::FakeCompressCert => {
                CraftClientExtension::raw(ExtensionType::CompressCertificate, vec![2, 0, 0])
            }
            Self::CompressCert(algorithms) => {
                if !craft_config.override_cert_compress {
                    store
                        .remove(&ExtensionType::CompressCertificate)
                        .ok_or(())?
                } else {
                    let missing_algorithms = algorithms
                        .iter()
                        .copied()
                        .filter(|algorithm| {
                            !config
                                .cert_decompressors
                                .iter()
                                .any(|decompressor| decompressor.algorithm() == *algorithm)
                        })
                        .collect::<Vec<_>>();
                    if !missing_algorithms.is_empty() {
                        return Err(CraftExtensionError::Fatal(Error::General(format!(
                            "craft fingerprint requires unsupported certificate compression algorithm(s): {missing_algorithms:?}"
                        ))));
                    }

                    CraftClientExtension::raw(
                        ExtensionType::CompressCertificate,
                        encode_certificate_compression_algorithms(algorithms),
                    )
                }
            }
            Self::Padding => {
                let psk_len = store
                    .get(&ExtensionType::PreSharedKey)
                    .map(|ext| 4 + ext.payload_len())
                    .unwrap_or(0);
                CraftClientExtension::padding(psk_len)
            }
            Self::Protocols(protocols) => {
                if !craft_config.override_alpn {
                    store
                        .remove(&ExtensionType::ALProtocolNegotiation)
                        .ok_or(())?
                } else {
                    if craft_config.strict_mode {
                        let offered = hello
                            .protocols
                            .as_deref()
                            .unwrap_or_default();
                        assert!(
                            protocols.len() == offered.len()
                                && protocols
                                    .iter()
                                    .zip(offered.iter())
                                    .all(|(a, b)| *a == b.as_ref())
                        );
                    }
                    CraftClientExtension::raw(
                        ExtensionType::ALProtocolNegotiation,
                        encode_protocol_list(protocols),
                    )
                }
            }
            Self::ProtocolsWithGrease(protocols) => {
                if !craft_config.override_alpn {
                    store
                        .remove(&ExtensionType::ALProtocolNegotiation)
                        .ok_or(())?
                } else {
                    CraftClientExtension::raw(
                        ExtensionType::ALProtocolNegotiation,
                        encode_protocol_list_with_grease(
                            protocols,
                            data.grease_seed
                                .get(BoringSslGreaseIndex::Alpn),
                        ),
                    )
                }
            }
            Self::DelegatedCredentials(delegated) => CraftClientExtension::raw(
                ExtensionType::DelegatedCredential,
                encode_signature_scheme_list(
                    delegated,
                    data.grease_seed
                        .get(BoringSslGreaseIndex::SignatureAlgorithm),
                ),
            ),
            Self::FakeDelegatedCredentials(delegated) => {
                let mut payload = Vec::new();
                {
                    let list = LengthPrefixedBuffer::new(ListLength::U16, &mut payload);
                    for scheme in *delegated {
                        scheme.encode(list.buf);
                    }
                }
                CraftClientExtension::raw(ExtensionType::DelegatedCredential, payload)
            }
            Self::RecordSizeLimit(limit) => CraftClientExtension::raw(
                ExtensionType::RecordSizeLimit,
                limit.to_be_bytes().to_vec(),
            ),
            Self::FakeRecordSizeLimit(limit) => CraftClientExtension::raw(
                ExtensionType::RecordSizeLimit,
                limit.to_be_bytes().to_vec(),
            ),
            Self::PresharedKeyModes(modes) => CraftClientExtension::raw(
                ExtensionType::PSKKeyExchangeModes,
                encode_psk_key_exchange_modes(
                    modes,
                    data.grease_seed
                        .get_psk_key_exchange_mode(),
                ),
            ),
            Self::PostHandshakeAuth => {
                CraftClientExtension::raw(ExtensionType::PostHandshakeAuth, Vec::new())
            }
            Self::ClientCertificateTypes(types) => CraftClientExtension::raw(
                ExtensionType::ClientCertificateType,
                encode_certificate_types(types),
            ),
            Self::ServerCertificateTypes(types) => CraftClientExtension::raw(
                ExtensionType::ServerCertificateType,
                encode_certificate_types(types),
            ),
            Self::CertificateAuthorities(authorities) => CraftClientExtension::raw(
                ExtensionType::CertificateAuthorities,
                encode_certificate_authorities(authorities),
            ),
            Self::QuicTransportParameters(params) => {
                CraftClientExtension::raw(ExtensionType::TransportParameters, params.to_vec())
            }
            Self::QuicTransportParametersLegacy(params) => CraftClientExtension::raw(
                ExtensionType::QuicTransportParametersLegacy,
                params.to_vec(),
            ),
            Self::TrustAnchors(anchors) => CraftClientExtension::raw(
                ExtensionType::TrustAnchors,
                encode_trust_anchors(anchors),
            ),
            Self::Pake(payload) => CraftClientExtension::raw(ExtensionType::Pake, payload.to_vec()),
        };

        Ok((ext, result))
    }
}

#[derive(Clone, Debug)]
pub struct CraftPadding {
    psk_len: usize,
}

impl CraftPadding {
    fn encode_extension(&self, typ: ExtensionType, bytes: &mut Vec<u8>) {
        let unpadded = self.psk_len + bytes.len();
        if !(unpadded > 0xff && unpadded < 0x200) {
            return;
        }

        typ.encode(bytes);
        let nested = LengthPrefixedBuffer::new(ListLength::U16, bytes);
        let mut padding_len = 0x200 - unpadded;
        if padding_len > 4 {
            padding_len -= 4;
        } else {
            padding_len = 1;
        }
        nested
            .buf
            .resize(nested.buf.len() + padding_len, 0);
    }
}

impl Codec<'_> for CraftPadding {
    fn encode(&self, bytes: &mut Vec<u8>) {
        let Some(unpadded_len) = bytes.len().checked_sub(4) else {
            return;
        };
        let unpadded = self.psk_len + unpadded_len;
        if unpadded > 0xff && unpadded < 0x200 {
            let mut padding_len = 0x200 - unpadded;
            if padding_len > 4 {
                padding_len -= 4;
            } else {
                padding_len = 1;
            }
            bytes.resize(bytes.len() + padding_len, 0);
        } else {
            bytes.resize(bytes.len() - 4, 0);
        }
    }

    fn read(_: &mut crate::msgs::Reader<'_>) -> Result<Self, crate::error::InvalidMessage> {
        Err(crate::error::InvalidMessage::MissingData("CraftPadding"))
    }
}

#[derive(Debug, Clone)]
pub enum ClientSessionTicket {
    Request,
    Offer(Payload<'static>),
}

#[derive(Debug, Clone)]
pub struct PayloadU16(pub Vec<u8>);

impl PayloadU16 {
    fn encode(&self, bytes: &mut Vec<u8>) {
        (self.0.len() as u16).encode(bytes);
        bytes.extend_from_slice(&self.0);
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ECPointFormat(pub u8);

impl ECPointFormat {
    #[allow(non_upper_case_globals)]
    pub const Uncompressed: Self = Self(0);
}

impl Codec<'_> for ECPointFormat {
    fn encode(&self, bytes: &mut Vec<u8>) {
        self.0.encode(bytes);
    }

    fn read(r: &mut crate::msgs::Reader<'_>) -> Result<Self, crate::error::InvalidMessage> {
        Ok(Self(u8::read(r)?))
    }
}

impl TlsListElement for ECPointFormat {
    const SIZE_LEN: ListLength = ListLength::NonZeroU8 {
        empty_error: crate::error::InvalidMessage::IllegalEmptyList("ECPointFormats"),
    };
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct PSKKeyExchangeMode(pub u8);

impl PSKKeyExchangeMode {
    pub const PSK_KE: Self = Self(0);
    pub const PSK_DHE_KE: Self = Self(1);
}

impl Codec<'_> for PSKKeyExchangeMode {
    fn encode(&self, bytes: &mut Vec<u8>) {
        self.0.encode(bytes);
    }

    fn read(r: &mut crate::msgs::Reader<'_>) -> Result<Self, crate::error::InvalidMessage> {
        Ok(Self(u8::read(r)?))
    }
}

impl TlsListElement for PSKKeyExchangeMode {
    const SIZE_LEN: ListLength = ListLength::NonZeroU8 {
        empty_error: crate::error::InvalidMessage::IllegalEmptyList("PskKeyExchangeModes"),
    };
}

#[derive(Debug, Clone)]
pub struct OcspCertificateStatusRequest {
    pub responder_ids: Vec<PayloadU16>,
    pub extensions: PayloadU16,
}

#[derive(Debug, Clone)]
pub enum CertificateStatusRequest {
    Ocsp(OcspCertificateStatusRequest),
}

impl CertificateStatusRequest {
    fn encode(&self, bytes: &mut Vec<u8>) {
        match self {
            Self::Ocsp(req) => {
                1u8.encode(bytes);
                {
                    let responders = LengthPrefixedBuffer::new(ListLength::U16, bytes);
                    for responder in &req.responder_ids {
                        responder.encode(responders.buf);
                    }
                }
                req.extensions.encode(bytes);
            }
        }
    }
}

#[derive(Debug, Clone)]
pub enum ClientExtension {
    Raw(ExtensionType, Vec<u8>),
    EcPointFormats(Vec<ECPointFormat>),
    SignatureAlgorithms(Vec<SignatureScheme>),
    SignatureAlgorithmsCert(Vec<SignatureScheme>),
    SessionTicket(ClientSessionTicket),
    ExtendedMasterSecretRequest,
    CertificateStatusRequest(CertificateStatusRequest),
    PresharedKeyModes(Vec<PSKKeyExchangeMode>),
    PostHandshakeAuth,
    ClientCertificateTypes(Vec<CertificateType>),
    ServerCertificateTypes(Vec<CertificateType>),
}

impl ClientExtension {
    fn to_wire_extension(&self) -> CraftClientExtension {
        match self {
            Self::Raw(typ, payload) => CraftClientExtension::raw(*typ, payload.clone()),
            Self::EcPointFormats(formats) => {
                CraftClientExtension::encoded(ExtensionType::ECPointFormats, formats)
            }
            Self::SignatureAlgorithms(schemes) => {
                CraftClientExtension::encoded(ExtensionType::SignatureAlgorithms, schemes)
            }
            Self::SignatureAlgorithmsCert(schemes) => {
                CraftClientExtension::encoded(ExtensionType::SignatureAlgorithmsCert, schemes)
            }
            Self::SessionTicket(ClientSessionTicket::Request) => {
                CraftClientExtension::raw(ExtensionType::SessionTicket, Vec::new())
            }
            Self::SessionTicket(ClientSessionTicket::Offer(payload)) => {
                CraftClientExtension::raw(ExtensionType::SessionTicket, payload.bytes().to_vec())
            }
            Self::ExtendedMasterSecretRequest => {
                CraftClientExtension::raw(ExtensionType::ExtendedMasterSecret, Vec::new())
            }
            Self::CertificateStatusRequest(req) => {
                let mut payload = Vec::new();
                req.encode(&mut payload);
                CraftClientExtension::raw(ExtensionType::StatusRequest, payload)
            }
            Self::PresharedKeyModes(modes) => CraftClientExtension::encoded(
                ExtensionType::PSKKeyExchangeModes,
                &PskKeyExchangeModes {
                    psk_dhe: modes.contains(&PSKKeyExchangeMode::PSK_DHE_KE),
                    psk: modes.contains(&PSKKeyExchangeMode::PSK_KE),
                },
            ),
            Self::PostHandshakeAuth => {
                CraftClientExtension::raw(ExtensionType::PostHandshakeAuth, Vec::new())
            }
            Self::ClientCertificateTypes(types) => CraftClientExtension::raw(
                ExtensionType::ClientCertificateType,
                encode_certificate_types(types),
            ),
            Self::ServerCertificateTypes(types) => CraftClientExtension::raw(
                ExtensionType::ServerCertificateType,
                encode_certificate_types(types),
            ),
        }
    }
}

#[derive(Debug, Clone)]
pub enum KeepExtension {
    Must(ExtensionType),
    Optional(ExtensionType),
    OrDefault(ExtensionType, ClientExtension),
}

#[derive(Debug, Clone)]
pub enum ExtensionSpec {
    Craft(CraftExtension),
    Rustls(ClientExtension),
    Keep(KeepExtension),
}

fn shuffle_extensions(extensions: &[ExtensionSpec], config: &ClientConfig) -> Vec<usize> {
    use ExtensionSpec::*;

    let mut to_shuffle = Vec::with_capacity(extensions.len());
    let mut do_not_shuffle = Vec::with_capacity(extensions.len());
    for (i, ext) in extensions.iter().enumerate() {
        match ext {
            Craft(CraftExtension::Grease1 | CraftExtension::Grease2)
            | Craft(CraftExtension::Padding)
            | Keep(KeepExtension::Optional(ExtensionType::PreSharedKey)) => {
                do_not_shuffle.push(i);
            }
            _ => to_shuffle.push(i),
        }
    }

    let rand_gen = config.provider().secure_random;
    for i in (1..to_shuffle.len()).rev() {
        let mut random_buf = [0u8; 4];
        rand_gen.fill(&mut random_buf).unwrap();
        let swap_idx = (u32::from_be_bytes(random_buf) as usize) % (i + 1);
        to_shuffle.swap(i, swap_idx);
    }

    for i in do_not_shuffle {
        to_shuffle.insert(i, i);
    }

    to_shuffle
}

#[derive(Debug, Clone, Default)]
pub struct Fingerprint {
    pub extensions: &'static [ExtensionSpec],
    pub shuffle_extensions: bool,
    pub cipher: &'static [GreaseOrCipher],
    pub ech_force_tls13: Option<bool>,
    pub ech_padding_style: EchPaddingStyle,
}

#[derive(Debug, Clone, Copy, Default, Eq, PartialEq)]
pub enum EchPaddingStyle {
    #[default]
    Standard,
    Nss,
}

impl Fingerprint {
    pub fn builder(&self) -> FingerprintBuilder {
        FingerprintBuilder {
            fingerprint: self.clone(),
            override_alpn: true,
            strict_mode: true,
            override_supported_curves: true,
            override_version: true,
            override_keyshare: true,
            override_cert_compress: true,
            override_suite: true,
            ech_force_tls13: self.ech_force_tls13,
            ech_padding_style: self.ech_padding_style,
            validation_error: None,
        }
    }

    fn patch_client_hello(
        &self,
        data: &mut CraftConnectionData,
        config: &ClientConfig,
        hrr: Option<&HelloRetryRequest>,
        hello: &mut ClientHelloPayload,
    ) -> CraftPatchResult {
        let Some(craft_config) = config.craft.get() else {
            return CraftPatchResult::default();
        };

        let mut store = wire_extension_store(&hello.extensions);

        if hrr.is_none() && self.shuffle_extensions {
            data.extension_order = shuffle_extensions(self.extensions, config);
        }

        let order = if data.extension_order.is_empty() {
            (0..self.extensions.len()).collect::<Vec<_>>()
        } else {
            data.extension_order.clone()
        };

        let mut result = CraftPatchResult::default();
        let mut output = Vec::new();
        for idx in order {
            let Some(spec) = self.extensions.get(idx) else {
                continue;
            };

            let extension = match spec {
                ExtensionSpec::Craft(ext) => {
                    let (ext, patch_result) =
                        match ext.to_wire_extension(data, config, &mut store, hello, hrr) {
                            Ok(result) => result,
                            Err(CraftExtensionError::Skip) => continue,
                            Err(CraftExtensionError::Fatal(error)) => {
                                result.error = Some(error);
                                break;
                            }
                        };
                    if patch_result.error.is_some() {
                        result.error = patch_result.error;
                        break;
                    };
                    if result.key_shares.is_empty() {
                        result.key_shares = patch_result.key_shares;
                    }
                    ext
                }
                ExtensionSpec::Rustls(ext) => ext.to_wire_extension(),
                ExtensionSpec::Keep(KeepExtension::Must(ext_type)) => {
                    match store.remove(ext_type) {
                        Some(ext) => ext,
                        None => {
                            if *ext_type == ExtensionType::ServerName && !config.enable_sni {
                                continue;
                            }
                            assert!(
                                !craft_config.strict_mode,
                                "expected extension {ext_type:?}, but rustls did not generate it"
                            );
                            continue;
                        }
                    }
                }
                ExtensionSpec::Keep(KeepExtension::Optional(ext_type)) => {
                    let Some(ext) = store.remove(ext_type) else {
                        continue;
                    };
                    ext
                }
                ExtensionSpec::Keep(KeepExtension::OrDefault(ext_type, default_ext)) => store
                    .remove(ext_type)
                    .unwrap_or_else(|| default_ext.to_wire_extension()),
            };

            output.push(extension);
        }

        hello.craft_extensions = Some(output);
        result
    }

    pub(crate) fn patch_cipher(
        &self,
        data: &mut CraftConnectionData,
        cipher_suites: &mut Vec<CipherSuite>,
    ) {
        *cipher_suites = self
            .cipher
            .iter()
            .map(|cipher| {
                cipher.val_or(
                    data.grease_seed
                        .get(BoringSslGreaseIndex::Cipher),
                )
            })
            .collect();
    }
}

#[derive(Debug, Clone)]
pub struct FingerprintBuilder {
    fingerprint: Fingerprint,
    override_alpn: bool,
    override_version: bool,
    override_supported_curves: bool,
    strict_mode: bool,
    override_keyshare: bool,
    override_cert_compress: bool,
    override_suite: bool,
    ech_force_tls13: Option<bool>,
    ech_padding_style: EchPaddingStyle,
    validation_error: Option<String>,
}

impl FingerprintBuilder {
    pub fn do_not_override_alpn(mut self) -> Self {
        self.override_alpn = false;
        self
    }

    pub fn do_not_override_versions(mut self) -> Self {
        self.override_version = false;
        self
    }

    pub fn do_not_override_certificate_compression(mut self) -> Self {
        self.override_cert_compress = false;
        self
    }

    pub fn dangerous_disable_override_supported_curves(mut self) -> Self {
        self.override_supported_curves = false;
        self
    }

    pub fn dangerous_disable_override_keyshare(mut self) -> Self {
        self.override_keyshare = false;
        self
    }

    pub fn dangerous_disable_override_suite(mut self) -> Self {
        self.override_suite = false;
        self
    }

    pub fn ech_force_tls13(&self) -> Option<bool> {
        self.ech_force_tls13
    }

    pub fn with_ech_force_tls13(mut self, ech_force_tls13: bool) -> Self {
        self.ech_force_tls13 = Some(ech_force_tls13);
        self
    }

    pub fn with_ech_padding_style(mut self, ech_padding_style: EchPaddingStyle) -> Self {
        self.ech_padding_style = ech_padding_style;
        self
    }

    pub fn dangerous_craft_test_mode(mut self) -> Self {
        self.strict_mode = false;
        self.override_alpn = false;
        self.override_version = false;
        self.override_cert_compress = false;
        self
    }

    fn build(self) -> CraftOptions {
        CraftOptions(Some(self))
    }

    pub(crate) fn patch_config(mut self, mut config: ClientConfig) -> ClientConfig {
        for ext in self.fingerprint.extensions.iter() {
            match ext {
                ExtensionSpec::Craft(CraftExtension::SupportedCurves(curves)) => {
                    if !self.override_supported_curves {
                        continue;
                    }

                    let mut provider = config.provider().as_ref().clone();
                    let mut kx_groups = provider.kx_groups.to_vec();
                    let mut grease_offset = 0;
                    let mut missing_offset = 0;
                    for (idx, curve) in curves.iter().enumerate() {
                        match curve {
                            Grease => {
                                grease_offset += 1;
                            }
                            GreaseOr::T(curve) => {
                                let desired_idx = idx - grease_offset - missing_offset;
                                if let Some(old_idx) = kx_groups
                                    .iter()
                                    .position(|v| v.name() == *curve)
                                {
                                    if desired_idx != old_idx {
                                        kx_groups.swap(desired_idx, old_idx);
                                    }
                                } else {
                                    missing_offset += 1;
                                    if let Some(group) =
                                        to_missing_kx_group(curve, provider.secure_random)
                                    {
                                        kx_groups.push(group);
                                    } else if self.strict_mode {
                                        self.validation_error = Some(format!(
                                            "craft fingerprint requires unsupported named group {curve:?}"
                                        ));
                                    }
                                }
                            }
                        }
                    }
                    provider.kx_groups = Cow::Owned(kx_groups);
                    config.set_provider_for_craft(Arc::new(provider));
                }
                ExtensionSpec::Craft(CraftExtension::Protocols(protocols)) => {
                    if self.override_alpn {
                        config.alpn_protocols = protocols
                            .iter()
                            .map(|protocol| ApplicationProtocol::from(protocol.to_vec()))
                            .collect();
                    }
                }
                ExtensionSpec::Craft(CraftExtension::ProtocolsWithGrease(protocols)) => {
                    if self.override_alpn {
                        config.alpn_protocols = protocols
                            .iter()
                            .filter_map(|protocol| match protocol {
                                GreaseOrProtocol::Grease => None,
                                GreaseOrProtocol::Protocol(protocol) => {
                                    Some(ApplicationProtocol::from(protocol.to_vec()))
                                }
                            })
                            .collect();
                    }
                }
                ExtensionSpec::Craft(CraftExtension::CompressCert(algorithms)) => {
                    if self.override_cert_compress {
                        config.cert_decompressors = compress::default_cert_decompressors()
                            .iter()
                            .copied()
                            .filter(|decompressor| algorithms.contains(&decompressor.algorithm()))
                            .collect();
                    }
                }
                _ => {}
            }
        }
        config.craft = self.build();
        config
    }
}

impl CraftOptions {
    pub(crate) fn ech_padding_style(&self) -> EchPaddingStyle {
        self.get()
            .map(|builder| builder.ech_padding_style)
            .unwrap_or_default()
    }
}

pub struct FingerprintSet {
    pub main: Fingerprint,
    pub test_alpn_http1: Fingerprint,
    pub test_no_alpn: Fingerprint,
}

impl core::ops::Deref for FingerprintSet {
    type Target = Fingerprint;

    fn deref(&self) -> &Self::Target {
        &self.main
    }
}

#[derive(Clone)]
pub(crate) struct CraftClientExtension {
    typ: ExtensionType,
    payload: CraftClientExtensionPayload,
}

impl Debug for CraftClientExtension {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("CraftClientExtension")
            .field("typ", &self.typ)
            .field("payload_len", &self.payload_len())
            .finish()
    }
}

#[derive(Clone, Debug)]
enum CraftClientExtensionPayload {
    Raw(Vec<u8>),
    Padding(CraftPadding),
}

impl CraftClientExtension {
    pub(crate) fn raw(typ: ExtensionType, payload: Vec<u8>) -> Self {
        Self {
            typ,
            payload: CraftClientExtensionPayload::Raw(payload),
        }
    }

    fn encoded<T>(typ: ExtensionType, payload: &T) -> Self
    where
        T: Codec<'static>,
    {
        let mut bytes = Vec::new();
        payload.encode(&mut bytes);
        Self::raw(typ, bytes)
    }

    fn padding(psk_len: usize) -> Self {
        Self {
            typ: ExtensionType::Padding,
            payload: CraftClientExtensionPayload::Padding(CraftPadding { psk_len }),
        }
    }

    pub(crate) fn extension_type(&self) -> ExtensionType {
        self.typ
    }

    pub(crate) fn raw_payload(&self) -> Option<&[u8]> {
        match &self.payload {
            CraftClientExtensionPayload::Raw(payload) => Some(payload),
            CraftClientExtensionPayload::Padding(_) => None,
        }
    }

    fn payload_len(&self) -> usize {
        match &self.payload {
            CraftClientExtensionPayload::Raw(payload) => payload.len(),
            CraftClientExtensionPayload::Padding(_) => 0,
        }
    }
}

impl Codec<'_> for CraftClientExtension {
    fn encode(&self, bytes: &mut Vec<u8>) {
        if let CraftClientExtensionPayload::Padding(padding) = &self.payload {
            padding.encode_extension(self.typ, bytes);
            return;
        }

        self.typ.encode(bytes);
        let nested = LengthPrefixedBuffer::new(ListLength::U16, bytes);
        match &self.payload {
            CraftClientExtensionPayload::Raw(payload) => nested.buf.extend_from_slice(payload),
            CraftClientExtensionPayload::Padding(_) => unreachable!(),
        }
    }

    fn read(_: &mut crate::msgs::Reader<'_>) -> Result<Self, crate::error::InvalidMessage> {
        Err(crate::error::InvalidMessage::MissingData(
            "CraftClientExtension",
        ))
    }
}

impl TlsListElement for CraftClientExtension {
    const SIZE_LEN: ListLength = ListLength::U16;
}

fn wire_extension_store(
    extensions: &ClientExtensions<'static>,
) -> HashMap<ExtensionType, CraftClientExtension> {
    let mut store = HashMap::new();
    for typ in extensions.used_extensions_in_encoding_order() {
        if let Some(ext) = encode_single_extension(extensions, typ) {
            store.insert(typ, ext);
        }
    }
    store
}

fn encode_single_extension(
    extensions: &ClientExtensions<'static>,
    typ: ExtensionType,
) -> Option<CraftClientExtension> {
    let mut one = ClientExtensions::default();
    one.clone_one(extensions, typ);
    let mut encoded = Vec::new();
    one.encode(&mut encoded);
    if encoded.len() < 6 {
        return None;
    }

    let outer_len = u16::from_be_bytes([encoded[0], encoded[1]]) as usize;
    if outer_len + 2 != encoded.len() {
        return None;
    }

    let actual = ExtensionType(u16::from_be_bytes([encoded[2], encoded[3]]));
    let payload_len = u16::from_be_bytes([encoded[4], encoded[5]]) as usize;
    if actual != typ || payload_len + 6 != encoded.len() {
        return None;
    }

    Some(CraftClientExtension::raw(typ, encoded[6..].to_vec()))
}
