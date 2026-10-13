package email

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/buildbuddy-io/buildbuddy/server/http/httpclient"
	"github.com/buildbuddy-io/buildbuddy/server/util/flag"
	"github.com/buildbuddy-io/buildbuddy/server/util/status"
)

var (
	// Generic email settings - if we switch providers, these flags would still
	// apply.
	defaultFromAddress = flag.Struct("notifications.email.default_from_address", Address{}, "Default sender address for email notifications.")

	// Provider-specific flags
	sendgridAPIKey = flag.String("notifications.email.sendgrid.api_key", "", "SendGrid API key used to send email notifications.", flag.Secret)
)

// IsConfigured returns whether email provider credentials are configured.
func IsConfigured() bool {
	return *sendgridAPIKey != ""
}

// Address identifies an email sender or recipient.
type Address struct {
	// Name is the display name associated with Email.
	Name string `json:"name" yaml:"name"`
	// Email is the email address.
	Email string `json:"email" yaml:"email"`
}

// Message describes an email notification.
type Message struct {
	// From is the sender address. If empty, the configured default sender is
	// used.
	From Address
	// ToAddresses are recipients who each receive their own addressed email.
	ToAddresses []Address
	// Subject is the email subject.
	Subject string
	// Body is the HTML email body.
	Body string
}

// Client sends email notifications.
type Client struct {
	client       *http.Client
	sendgridHost string
}

// ClientConfig configures a Client.
type ClientConfig struct {
	// HTTPClient is the HTTP client used to call SendGrid. If nil, a default
	// BuildBuddy HTTP client is used.
	HTTPClient *http.Client
	// SendGridHost overrides the SendGrid API host. If empty, SendGrid's
	// default host is used.
	SendGridHost string
}

// NewClient returns an email notification client.
func NewClient(config ClientConfig) *Client {
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = httpclient.New(nil, "sendgrid")
	}
	return &Client{
		client:       httpClient,
		sendgridHost: config.SendGridHost,
	}
}

// Send sends msg through the configured backend using the configured
// notification flags.
func (c *Client) Send(ctx context.Context, msg *Message) error {
	if *sendgridAPIKey == "" {
		return status.FailedPreconditionError("SendGrid API key is required")
	}
	email, err := buildEmail(msg)
	if err != nil {
		return err
	}
	body, err := json.Marshal(email)
	if err != nil {
		return status.UnavailableErrorf("send email through SendGrid: %s", err)
	}
	host := c.sendgridHost
	if host == "" {
		host = "https://api.sendgrid.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/v3/mail/send", bytes.NewReader(body))
	if err != nil {
		return status.UnavailableErrorf("send email through SendGrid: %s", err)
	}
	request.Header.Set("Authorization", "Bearer "+*sendgridAPIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "sendgrid/3.16.1;go")
	resp, err := c.client.Do(request)
	if err != nil {
		return status.UnavailableErrorf("send email through SendGrid: %s", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return status.UnavailableErrorf("send email through SendGrid: %s", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return status.UnavailableErrorf("SendGrid returned HTTP %d: %s", resp.StatusCode, responseBody)
	}
	return nil
}

// These types describe the subset of the SendGrid mail API used here.
// Names are omitted when empty, matching the previous SDK encoding.
type mailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type personalization struct {
	To []mailAddress `json:"to"`
}

type content struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type mailRequest struct {
	From             mailAddress       `json:"from"`
	Subject          string            `json:"subject"`
	Personalizations []personalization `json:"personalizations"`
	Content          []content         `json:"content"`
}

func buildEmail(msg *Message) (*mailRequest, error) {
	if msg == nil {
		return nil, status.InvalidArgumentError("message is required")
	}
	from := msg.From
	if from.Email == "" {
		from = defaultFrom()
	}
	if from.Email == "" {
		return nil, status.FailedPreconditionError("SendGrid sender email address is required")
	}
	recipients := msg.ToAddresses
	if len(recipients) == 0 {
		return nil, status.InvalidArgumentError("recipient is required")
	}
	for _, recipient := range recipients {
		if recipient.Email == "" {
			return nil, status.InvalidArgumentError("recipient email address is required")
		}
	}
	if msg.Subject == "" {
		return nil, status.InvalidArgumentError("subject is required")
	}
	if msg.Body == "" {
		return nil, status.InvalidArgumentError("body is required")
	}

	email := &mailRequest{
		From:    toMailAddress(from),
		Subject: msg.Subject,
		Content: []content{{Type: "text/html", Value: msg.Body}},
	}
	for _, recipient := range recipients {
		// Separate personalizations keep recipients private from one another.
		email.Personalizations = append(email.Personalizations, personalization{To: []mailAddress{toMailAddress(recipient)}})
	}

	return email, nil
}

func defaultFrom() Address {
	return *defaultFromAddress
}

func toMailAddress(address Address) mailAddress {
	return mailAddress(address)
}
