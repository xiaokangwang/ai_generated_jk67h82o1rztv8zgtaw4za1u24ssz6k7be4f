// These tests exercise craft fingerprints against real public TLS
// deployments. They are intentionally in connect-tests because they require
// network access and are not part of the default local unit test path.

use std::io::{ErrorKind, Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::time::Duration;

use rustls::craft;
use rustls::pki_types::ServerName;
use rustls::{ClientConfig, RootCertStore};
use rustls_util::Stream;

#[test]
fn chrome_108_fingerprint_connects_to_google_frontend() {
    check(
        "www.google.com",
        craft::CHROME_108
            .test_alpn_http1
            .builder(),
    );
}

#[test]
fn firefox_105_fingerprint_connects_to_cloudflare_frontend() {
    check(
        "www.cloudflare.com",
        craft::FIREFOX_105
            .test_alpn_http1
            .builder()
            .dangerous_disable_override_keyshare(),
    );
}

#[test]
fn safari_17_1_fingerprint_connects_to_wikipedia_frontend() {
    check(
        "www.wikipedia.org",
        craft::SAFARI_17_1
            .test_alpn_http1
            .builder(),
    );
}

fn check(hostname: &'static str, fingerprint: craft::FingerprintBuilder) {
    let root_store = RootCertStore {
        roots: webpki_roots::TLS_SERVER_ROOTS.into(),
    };
    let config = Arc::new(
        ClientConfig::builder(rustls_aws_lc_rs::DEFAULT_PROVIDER.into())
            .with_root_certificates(root_store)
            .with_no_client_auth()
            .unwrap()
            .with_fingerprint(fingerprint),
    );
    let server_name = ServerName::try_from(hostname)
        .unwrap()
        .to_owned();
    let mut conn = config
        .connect(server_name)
        .build()
        .unwrap();
    let mut sock = TcpStream::connect((hostname, 443)).unwrap();
    sock.set_read_timeout(Some(Duration::from_secs(15)))
        .unwrap();
    sock.set_write_timeout(Some(Duration::from_secs(15)))
        .unwrap();

    let mut tls = Stream::new(&mut conn, &mut sock);
    write!(
        tls,
        "GET / HTTP/1.1\r\nHost: {hostname}\r\nConnection: close\r\nAccept-Encoding: identity\r\n\r\n"
    )
    .unwrap();

    let mut response = String::new();
    match tls.read_to_string(&mut response) {
        Ok(_) => {}
        Err(err) if err.kind() == ErrorKind::UnexpectedEof && response.starts_with("HTTP/1.") => {}
        Err(err) => panic!("failed to read response from {hostname}: {err:?}"),
    }
    assert!(
        response.starts_with("HTTP/1."),
        "unexpected response from {hostname}: {response:?}"
    );
}
