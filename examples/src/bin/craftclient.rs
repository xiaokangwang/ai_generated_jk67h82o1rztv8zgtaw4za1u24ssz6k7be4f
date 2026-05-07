//! craftclient

use rustls::craft::GreaseOr::Grease;
use rustls::craft::{
    ClientExtension, CraftExtension, ECPointFormat, ExtensionSpec, Fingerprint, GreaseOrCurve,
    GreaseOrVersion, KeepExtension, PSKKeyExchangeMode,
};
use rustls::{ExtensionType, NamedGroup, ProtocolVersion, RootCertStore, SignatureScheme, craft};
use rustls_util::Stream;
use std::io::{Read, Write, stdout};
use std::net::TcpStream;
use std::sync::Arc;
use std::sync::LazyLock;

pub static CUSTOM_EXTENSION: LazyLock<Vec<ExtensionSpec>> = LazyLock::new(|| {
    use ExtensionSpec::*;
    use KeepExtension::*;
    vec![
        Keep(Must(ExtensionType::ServerName)),
        Rustls(ClientExtension::ExtendedMasterSecretRequest),
        Craft(CraftExtension::RenegotiationInfo),
        Craft(CraftExtension::SupportedCurves(&[
            Grease,
            GreaseOrCurve::T(NamedGroup::X25519),
            GreaseOrCurve::T(NamedGroup::secp384r1),
        ])),
        Rustls(ClientExtension::EcPointFormats(vec![
            ECPointFormat::Uncompressed,
        ])),
        Craft(CraftExtension::Protocols(&[b"http/1.1"])),
        Rustls(ClientExtension::SignatureAlgorithms(
            [
                SignatureScheme::ECDSA_NISTP256_SHA256,
                SignatureScheme::ECDSA_NISTP384_SHA384,
                SignatureScheme::RSA_PSS_SHA256,
                SignatureScheme::RSA_PSS_SHA384,
                SignatureScheme::RSA_PKCS1_SHA256,
                SignatureScheme::RSA_PKCS1_SHA384,
                SignatureScheme::RSA_PKCS1_SHA1,
            ]
            .to_vec(),
        )),
        Craft(CraftExtension::KeyShare(&[GreaseOrCurve::T(
            NamedGroup::X25519,
        )])),
        Rustls(ClientExtension::PresharedKeyModes(vec![
            PSKKeyExchangeMode::PSK_DHE_KE,
        ])),
        Craft(CraftExtension::SupportedVersions(&[
            Grease,
            GreaseOrVersion::T(ProtocolVersion::TLSv1_3),
            GreaseOrVersion::T(ProtocolVersion::TLSv1_2),
        ])),
        Craft(CraftExtension::Padding),
    ]
});

pub static CUSTOM_FINGERPRINT: LazyLock<Fingerprint> = LazyLock::new(|| Fingerprint {
    extensions: CUSTOM_EXTENSION.as_slice(),
    cipher: craft::CHROME_CIPHER.as_slice(),
    shuffle_extensions: false,
});

fn main() {
    fn request(fingerprint: &Fingerprint) {
        let root_store = RootCertStore {
            roots: webpki_roots::TLS_SERVER_ROOTS.into(),
        };
        let config = rustls::ClientConfig::builder(rustls_aws_lc_rs::DEFAULT_PROVIDER.into())
            .with_root_certificates(root_store)
            .with_no_client_auth()
            .unwrap()
            .with_fingerprint(fingerprint.builder());

        let server_name = "chat.openai.com".try_into().unwrap();
        let mut conn = Arc::new(config)
            .connect(server_name)
            .build()
            .unwrap();
        let mut sock = TcpStream::connect("chat.openai.com:443").unwrap();
        let mut tls = Stream::new(&mut conn, &mut sock);
        tls.write_all(
            concat!(
                "GET /auth/login HTTP/1.1\r\n",
                "Host: chat.openai.com\r\n",
                "Connection: close\r\n",
                "Accept-Encoding: identity\r\n",
                "User-Agent: Mozilla/5.0 (Windows NT 10.0; WOW64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/113.0.5666.197 Safari/537.36\r\n",
                "\r\n"
            )
            .as_bytes(),
        )
        .unwrap();
        let mut plaintext = Vec::new();
        tls.read_to_end(&mut plaintext).unwrap();
        stdout()
            .write_all(
                &plaintext[..1 + plaintext
                    .iter()
                    .position(|c| *c == b'\n')
                    .unwrap()],
            )
            .unwrap();
    }

    request(&craft::CHROME_108.test_alpn_http1);
    request(&CUSTOM_FINGERPRINT);
}
