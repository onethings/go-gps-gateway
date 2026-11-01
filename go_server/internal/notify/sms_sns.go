package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"
)

// sendSNS publishes one SMS through the AWS Simple Notification Service using a
// signed query (SigV4) request. No AWS SDK is pulled in for one API call.
func (s *Sms) sendSNS(ctx context.Context, phone, message string) error {
	endpoint := s.config.AWSEndpoint
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://sns.%s.amazonaws.com/", s.config.AWSRegion)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("sms: sns endpoint %q: %w", endpoint, err)
	}
	if parsed.Host == "" {
		return fmt.Errorf("sms: sns endpoint %q has no host", endpoint)
	}

	form := url.Values{
		"Action":      {"Publish"},
		"PhoneNumber": {phone},
		"Message":     {message},
		"Version":     {"2010-03-31"},
	}
	body := form.Encode()
	contentType := "application/x-www-form-urlencoded; charset=utf-8"

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(body)

	canonicalHeaders := "content-type:" + contentType + "\n" +
		"host:" + parsed.Host + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "content-type;host;x-amz-date"
	canonicalRequest := "POST\n/\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash

	scope := dateStamp + "/" + s.config.AWSRegion + "/sns/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex(canonicalRequest)
	key := hmacSHA256([]byte("AWS4"+s.config.AWSSecret), dateStamp)
	key = hmacSHA256(key, s.config.AWSRegion)
	key = hmacSHA256(key, "sns")
	key = hmacSHA256(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))

	authorization := "AWS4-HMAC-SHA256 Credential=" + s.config.AWSAccess + "/" + scope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature

	return postBody(ctx, s.config.Client, "sms", endpoint, contentType, []byte(body),
		map[string]string{
			"Authorization": authorization,
			"X-Amz-Date":    amzDate,
		})
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}
