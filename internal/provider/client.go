package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

type IssueCertRequest struct {
	CommonName  string   `json:"common_name"`
	TTL         string   `json:"ttl,omitempty"`
	AltNames    []string `json:"alt_names,omitempty"`
	ImportToACM bool     `json:"import_to_acm,omitempty"`
}

type IssueCertResponse struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
}

type CertificateRecord struct {
	RequestID         string `json:"request_id"`
	CN                string `json:"cn"`
	Status            string `json:"status"`
	TTL               string `json:"ttl"`
	SerialNumber      string `json:"serial_number"`
	SecretARN         string `json:"secret_arn"`
	ExpiryTimestamp   int64  `json:"expiry_timestamp"`
	ACMCertificateARN string `json:"acm_certificate_arn,omitempty"`
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

func (c *Client) GetCertificate(ctx context.Context, requestID string) (*CertificateRecord, error) {
	var record CertificateRecord
	if err := c.do(ctx, http.MethodGet, "/certificates/"+requestID, nil, &record); err != nil {
		return nil, err
	}
	return &record, nil
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
		return fmt.Errorf("harbour API returned %d: %s", resp.StatusCode, string(respBytes))
	}

	if out != nil {
		if err := json.Unmarshal(respBytes, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}

	return nil
}
