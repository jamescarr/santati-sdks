//! What the `events` and `schemas` operations share: one response read to the
//! end, the cursor reader and the path-segment encoder.

use reqwest::header::HeaderMap;
use reqwest::{RequestBuilder, Response};

use crate::error::Error;

/// One response, read to the end so the connection is reusable.
pub(crate) struct RawResponse {
    pub(crate) status: u16,
    pub(crate) headers: HeaderMap,
    pub(crate) body: Vec<u8>,
}

/// Send one request, mapping a failure to answer at all to a transport error.
pub(crate) async fn send(request: RequestBuilder) -> Result<RawResponse, Error> {
    let response: Response = request
        .send()
        .await
        .map_err(|error| Error::transport(error.to_string()))?;
    let status = response.status().as_u16();
    let headers = response.headers().clone();
    let body = response
        .bytes()
        .await
        .map_err(|error| Error::transport(error.to_string()))?
        .to_vec();
    Ok(RawResponse {
        status,
        headers,
        body,
    })
}

/// The decoded `cursor` of a page's `next` URL, when it carries one.
pub(crate) fn cursor_from_url(next: &str) -> Option<String> {
    let url = url::Url::parse(next).ok()?;
    url.query_pairs()
        .find(|(key, _)| key == "cursor")
        .map(|(_, value)| value.into_owned())
}

/// `segment` percent-encoded for one URL path segment: everything but
/// `A-Za-z0-9-._~` becomes `%XX` (uppercase hex), so a `/` or `?` in a value
/// cannot change the path.
pub(crate) fn encode_segment(segment: &str) -> String {
    let mut encoded = String::with_capacity(segment.len());
    for byte in segment.bytes() {
        if byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'.' | b'_' | b'~') {
            encoded.push(char::from(byte));
        } else {
            encoded.push_str(&format!("%{byte:02X}"));
        }
    }
    encoded
}

#[cfg(test)]
mod tests {
    use super::encode_segment;

    #[test]
    fn keeps_unreserved_and_encodes_the_rest() {
        assert_eq!(encode_segment("invoice.voided"), "invoice.voided");
        assert_eq!(encode_segment("a-b_c~d"), "a-b_c~d");
        assert_eq!(encode_segment("a/b?c d"), "a%2Fb%3Fc%20d");
        assert_eq!(encode_segment("é"), "%C3%A9");
    }
}
