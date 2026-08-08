// Package mailfetch reads Outlook mailboxes through Microsoft Graph or IMAP
// according to the permissions carried by each refresh token.
package mailfetch

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"
)

var (
	ErrMissingCreds  = errors.New("client_id / refresh_token 必填")
	ErrAuthFailed    = errors.New("邮箱鉴权失败")
	ErrAuthTemporary = errors.New("邮箱鉴权服务暂时不可用")
)

// Account contains the OAuth credentials for one mailbox.
type Account struct {
	Email        string
	ClientID     string
	RefreshToken string
}

// Message is a mailbox message. ListMessages only fills the headers; the body
// is fetched on demand by GetMessage.
type Message struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	FromName   string    `json:"from_name"`
	Subject    string    `json:"subject"`
	ReceivedAt time.Time `json:"received_at"`
	HTML       string    `json:"html,omitempty"`
	Text       string    `json:"text,omitempty"`
}

// Client is safe for concurrent use. Access tokens are cached by refresh
// token together with the protocol implied by the granted OAuth scopes.
type Client struct {
	http     *http.Client
	tokMu    sync.Mutex
	tokens   map[string]cachedToken
	workMu   sync.Mutex
	inflight map[string]*tokenCall
	imapDial imapDialFunc
}

func New() *Client {
	return &Client{
		http:     &http.Client{Timeout: 15 * time.Second},
		tokens:   map[string]cachedToken{},
		inflight: map[string]*tokenCall{},
		imapDial: dialOutlookIMAP,
	}
}

// Verify checks the actual mail protocol, not just whether the OAuth token
// endpoint accepts the refresh token.
func (c *Client) Verify(ctx context.Context, acc Account) error {
	if err := validateAccount(acc); err != nil {
		return err
	}
	c.invalidate(acc.RefreshToken)
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 800 * time.Millisecond):
			}
		}
		tok, tokenErr := c.accessToken(ctx, acc)
		if tokenErr != nil {
			err = tokenErr
		} else if tok.protocol == protocolIMAP {
			err = c.verifyIMAP(ctx, acc, tok.access)
		} else {
			err = c.verifyGraph(ctx, tok.access)
		}
		if err == nil {
			return nil
		}
		if errors.Is(err, errIMAPAuthRejected) {
			c.invalidate(acc.RefreshToken)
			continue
		}
		if !errors.Is(err, ErrAuthTemporary) {
			return err
		}
		c.invalidate(acc.RefreshToken)
	}
	return err
}

// ListMessages returns the newest headers from Inbox and Junk, sorted newest
// first. Graph and IMAP IDs remain opaque to callers.
func (c *Client) ListMessages(ctx context.Context, acc Account, limit int) ([]Message, error) {
	if err := validateAccount(acc); err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 20
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.accessToken(ctx, acc)
		if err != nil {
			return nil, err
		}
		var messages []Message
		if tok.protocol == protocolIMAP {
			messages, err = c.listIMAPMessages(ctx, acc, tok.access, limit)
		} else {
			messages, err = c.listGraphMessages(ctx, tok.access, limit)
		}
		if !errors.Is(err, errTokenUnauthorized) && !errors.Is(err, errIMAPAuthRejected) {
			return messages, err
		}
		c.invalidate(acc.RefreshToken)
	}
	return nil, ErrAuthFailed
}

// GetMessage fetches one complete message. IMAP IDs encode the mailbox and
// immutable UID; Graph IDs are passed through unchanged.
func (c *Client) GetMessage(ctx context.Context, acc Account, msgID string) (Message, error) {
	if err := validateAccount(acc); err != nil {
		return Message{}, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := c.accessToken(ctx, acc)
		if err != nil {
			return Message{}, err
		}
		var message Message
		if tok.protocol == protocolIMAP {
			message, err = c.getIMAPMessage(ctx, acc, tok.access, msgID)
		} else {
			message, err = c.getGraphMessage(ctx, tok.access, msgID)
		}
		if !errors.Is(err, errTokenUnauthorized) && !errors.Is(err, errIMAPAuthRejected) {
			return message, err
		}
		c.invalidate(acc.RefreshToken)
	}
	return Message{}, ErrAuthFailed
}

func validateAccount(acc Account) error {
	if acc.ClientID == "" || acc.RefreshToken == "" {
		return ErrMissingCreds
	}
	return nil
}

func sortAndLimit(messages []Message, limit int) []Message {
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].ReceivedAt.After(messages[j].ReceivedAt)
	})
	if len(messages) > limit {
		return messages[:limit]
	}
	return messages
}
