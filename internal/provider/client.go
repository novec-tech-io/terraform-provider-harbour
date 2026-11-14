package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

type Client struct {
	endpoint   string
	region     string
	httpClient *http.Client
	awsCfg     aws.Config
}

func NewClient(endpoint, region string, awsCfg aws.Config) *Client {
	return &Client{
		endpoint:   strings.TrimRight(endpoint, "/"),
		region:     region,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		awsCfg:     awsCfg,
	}
}

type NotFoundError struct {
	Message string
}

func (e *NotFoundError) Error() string { return e.Message }

type ConflictError struct {
	Message string
}

func (e *ConflictError) Error() string { return e.Message }

// APIError is any non-2xx, non-404/409 response from the Harbour API. Error()
// keeps the exact "harbour API returned <status>: <body>" text callers have
// always seen; Status and Messages let callers (the renew action) show the
// API's own message instead of raw JSON.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("harbour API returned %d: %s", e.Status, e.Body)
}

// Messages returns the API's `{"errors": [...]}` strings, or the raw body if
// it isn't in that shape.
func (e *APIError) Messages() string {
	var parsed struct {
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(e.Body), &parsed); err == nil && len(parsed.Errors) > 0 {
		return strings.Join(parsed.Errors, "; ")
	}
	return e.Body
}

type IssueCertRequest struct {
	CommonName        string   `json:"common_name"`
	TTL               string   `json:"ttl,omitempty"`
	AltNames          []string `json:"alt_names,omitempty"`
	ExportToACM       bool     `json:"export_to_acm,omitempty"`
	CSR               string   `json:"csr,omitempty"`
	DeliveryAccountID string   `json:"delivery_account_id,omitempty"`
	AllowDuplicateCN  bool     `json:"allow_duplicate_cn,omitempty"`
}

type IssueCertResponse struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

type CertificateRecord struct {
	RequestID         string   `json:"request_id"`
	CN                string   `json:"cn"`
	Status            string   `json:"status"`
	TTL               string   `json:"ttl"`
	SerialNumber      string   `json:"serial_number"`
	SecretARN         string   `json:"secret_arn"`
	ExpiryTimestamp   int64    `json:"expiry_timestamp"`
	ACMCertificateARN string   `json:"acm_certificate_arn,omitempty"`
	SANs              []string `json:"sans,omitempty"`
	IssuanceMethod    string   `json:"issuance_method,omitempty"`
	AutoRenew         string   `json:"auto_renew,omitempty"`
	// RenewedToRequestID is set on a record whose certificate was renewed:
	// renewal is write-once (a new record per renewal), so the original
	// request_id a Terraform resource was created with goes `superseded` and
	// points forward at the live successor.
	RenewedToRequestID string `json:"renewed_to_request_id,omitempty"`
}

type ImportCertRequest struct {
	ACMCertificateARN string `json:"acm_certificate_arn"`
	AutoRenew         *bool  `json:"auto_renew,omitempty"`
}

type CAResponse struct {
	Roots         []string `json:"roots"`
	Intermediates []string `json:"intermediates"`
	ChainPEM      string   `json:"chain_pem"`
}

type RevokeRequest struct {
	RequestID    string `json:"request_id,omitempty"`
	SerialNumber string `json:"serial_number,omitempty"`
}

func (c *Client) IssueCertificate(ctx context.Context, req IssueCertRequest) (*IssueCertResponse, error) {
	var resp IssueCertResponse
	if err := c.do(ctx, http.MethodPost, "/certificates", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) ImportCertificate(ctx context.Context, req ImportCertRequest) (*CertificateRecord, error) {
	var record CertificateRecord
	if err := c.do(ctx, http.MethodPost, "/certificates/import", req, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (c *Client) GetCertificate(ctx context.Context, requestID string) (*CertificateRecord, error) {
	var record CertificateRecord
	if err := c.do(ctx, http.MethodGet, "/certificates/"+requestID, nil, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// RenewCertResponse is POST /certificates/{request_id}/renew's success body
// (KAN-166): the old record is now superseded, the new one is the live cert.
type RenewCertResponse struct {
	OldRequestID string            `json:"old_request_id"`
	NewRequestID string            `json:"new_request_id"`
	Certificate  CertificateRecord `json:"certificate"`
}

// RenewCertificate force-renews one issued certificate now (KAN-166). The
// API answers 404 (unknown), 409 (not issued / CSR-issued / a concurrent
// renewal), 429 (cooldown or daily issuance cap) or 502 (renewal failed; the
// existing certificate is unchanged and still valid).
func (c *Client) RenewCertificate(ctx context.Context, requestID string) (*RenewCertResponse, error) {
	var out RenewCertResponse
	if err := c.do(ctx, http.MethodPost, "/certificates/"+requestID+"/renew", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// maxRenewalChainHops bounds GetCurrentCertificate's walk. A certificate
// renewed once a day for years would still be far under this; the cap exists
// to turn a corrupt (looping) chain into an error instead of a hang.
const maxRenewalChainHops = 50

// GetCurrentCertificate returns the live record for a certificate lineage,
// following renewed_to_request_id forward from requestID until it reaches a
// record that has not been superseded. Harbour renewals are write-once, so a
// Terraform resource created with request_id X is, after its first renewal,
// bound to a dead (`superseded`) record -- every Read and Delete must resolve
// to the current record first or they operate on the wrong one (KAN-171).
//
// A missing starting record is a *NotFoundError, as with GetCertificate. A
// successor that cannot be found mid-chain is a plain error, deliberately
// not a NotFoundError: callers treat that as "the resource is gone" and drop
// it from state, which is wrong when the original record clearly exists.
func (c *Client) GetCurrentCertificate(ctx context.Context, requestID string) (*CertificateRecord, error) {
	seen := map[string]bool{}
	id := requestID
	for hop := 0; hop <= maxRenewalChainHops; hop++ {
		if seen[id] {
			return nil, fmt.Errorf("renewal chain starting at %s loops back to %s", requestID, id)
		}
		seen[id] = true

		record, err := c.GetCertificate(ctx, id)
		if err != nil {
			var nfe *NotFoundError
			if hop > 0 && errors.As(err, &nfe) {
				return nil, fmt.Errorf("certificate %s was renewed to %s, which could not be found", requestID, id)
			}
			return nil, err
		}
		if record.Status != "superseded" || record.RenewedToRequestID == "" {
			return record, nil
		}
		id = record.RenewedToRequestID
	}
	return nil, fmt.Errorf("renewal chain starting at %s is longer than %d records", requestID, maxRenewalChainHops)
}

func (c *Client) GetCA(ctx context.Context) (*CAResponse, error) {
	var ca CAResponse
	if err := c.do(ctx, http.MethodGet, "/ca", nil, &ca); err != nil {
		return nil, err
	}
	return &ca, nil
}

func (c *Client) RevokeCertificate(ctx context.Context, req RevokeRequest) error {
	return c.do(ctx, http.MethodPost, "/certificates/revoke", req, nil)
}

func (c *Client) PollCertificate(ctx context.Context, requestID string) (*CertificateRecord, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	deadline := time.After(5 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("timed out waiting for certificate %s to be issued", requestID)
		case <-ticker.C:
			record, err := c.GetCertificate(ctx, requestID)
			if err != nil {
				return nil, err
			}
			switch record.Status {
			case "issued":
				return record, nil
			case "failed":
				return nil, fmt.Errorf("certificate issuance failed for request %s", requestID)
			}
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var bodyBytes []byte
	var err error

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshalling request body: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	hash := sha256.Sum256(bodyBytes)
	bodyHash := hex.EncodeToString(hash[:])

	creds, err := c.awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("retrieving AWS credentials: %w", err)
	}

	if err := v4.NewSigner().SignHTTP(ctx, creds, req, bodyHash, "execute-api", c.region, time.Now()); err != nil {
		return fmt.Errorf("signing request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return &NotFoundError{Message: fmt.Sprintf("not found: %s", string(respBytes))}
	}
	if resp.StatusCode == http.StatusConflict {
		return &ConflictError{Message: fmt.Sprintf("conflict: %s", string(respBytes))}
	}
	if resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(respBytes)}
	}

	if out != nil {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}
