package ai

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 signing for the Bedrock Converse API. The AWS SDK
// performs this internally upstream; the Go port implements the documented
// algorithm (canonical request, string to sign, derived signing key).

// AWSCredentials is one resolved AWS credential set.
type AWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// IsZero reports whether the credential set is empty.
func (c AWSCredentials) IsZero() bool {
	return c.AccessKeyID == "" || c.SecretAccessKey == ""
}

// AWSSignatureResult is the outcome of signing one request.
type AWSSignatureResult struct {
	Authorization string
	SignedHeaders string
	Canonical     string
	StringToSign  string
	Signature     string
}

// SignAWSRequest signs an HTTP request with SigV4 for the given service and
// region, using the request body's SHA-256 hash as the payload hash. The
// request's Host header is included in the signature.
func SignAWSRequest(request *http.Request, body []byte, credentials AWSCredentials, service, region string, now time.Time) (*AWSSignatureResult, error) {
	if request == nil {
		return nil, fmt.Errorf("request is required")
	}
	if credentials.IsZero() {
		return nil, fmt.Errorf("AWS credentials are required")
	}
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	payloadHash := sha256Hex(body)
	request.Header.Set("x-amz-date", amzDate)
	if credentials.SessionToken != "" {
		request.Header.Set("x-amz-security-token", credentials.SessionToken)
	}

	host := request.Host
	if host == "" {
		host = request.URL.Host
	}

	// Canonical headers: host, all x-amz-* headers, and content-type when set.
	headerValues := map[string]string{"host": host}
	for name, values := range request.Header {
		lower := strings.ToLower(name)
		if lower == "content-type" || strings.HasPrefix(lower, "x-amz-") {
			headerValues[lower] = strings.Join(values, ",")
		}
	}
	headerValues["x-amz-date"] = amzDate
	if credentials.SessionToken != "" {
		headerValues["x-amz-security-token"] = credentials.SessionToken
	}
	names := make([]string, 0, len(headerValues))
	for name := range headerValues {
		names = append(names, name)
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headerValues[name]))
		canonicalHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonicalURI := request.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}
	canonicalQuery := canonicalizeAWSQuery(request.URL)

	canonicalRequest := strings.Join([]string{
		request.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveAWSSigningKey(credentials.SecretAccessKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	authorization := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		credentials.AccessKeyID, scope, signedHeaders, signature)
	request.Header.Set("Authorization", authorization)

	return &AWSSignatureResult{
		Authorization: authorization,
		SignedHeaders: signedHeaders,
		Canonical:     canonicalRequest,
		StringToSign:  stringToSign,
		Signature:     signature,
	}, nil
}

// deriveAWSSigningKey derives the SigV4 signing key.
func deriveAWSSigningKey(secret, dateStamp, region, service string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, service)
	return hmacSHA256(serviceKey, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// canonicalizeAWSQuery renders the canonical query string: parameters sorted by
// name (then value), each name and value URI-encoded (space as %20).
func canonicalizeAWSQuery(requestURL *url.URL) string {
	if requestURL == nil || requestURL.RawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(requestURL.RawQuery)
	if err != nil {
		return ""
	}
	type pair struct{ name, value string }
	var pairs []pair
	for name, entries := range values {
		for _, value := range entries {
			pairs = append(pairs, pair{name, value})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].name != pairs[j].name {
			return pairs[i].name < pairs[j].name
		}
		return pairs[i].value < pairs[j].value
	})
	parts := make([]string, 0, len(pairs))
	for _, entry := range pairs {
		parts = append(parts, awsURIEncode(entry.name)+"="+awsURIEncode(entry.value))
	}
	return strings.Join(parts, "&")
}

// awsURIEncode encodes per SigV4: unreserved characters are kept, everything
// else is percent-encoded (including "/" in query values).
func awsURIEncode(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}
