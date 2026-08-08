package mailfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	tokenURL   = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	graphScope = "https://graph.microsoft.com/.default"
)

type mailProtocol uint8

const (
	protocolGraph mailProtocol = iota
	protocolIMAP
)

type cachedToken struct {
	access    string
	protocol  mailProtocol
	expiresAt time.Time
}

type tokenCall struct {
	done chan struct{}
	tok  cachedToken
	err  error
}

type oauthResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func (c *Client) accessToken(ctx context.Context, acc Account) (cachedToken, error) {
	if err := validateAccount(acc); err != nil {
		return cachedToken{}, err
	}
	c.tokMu.Lock()
	if tok, ok := c.tokens[acc.RefreshToken]; ok && time.Until(tok.expiresAt) > time.Minute {
		c.tokMu.Unlock()
		return tok, nil
	}
	c.tokMu.Unlock()

	// Collapse concurrent refreshes for the same mailbox credential. The
	// token endpoint otherwise throttles large verification batches easily.
	c.workMu.Lock()
	if call, ok := c.inflight[acc.RefreshToken]; ok {
		c.workMu.Unlock()
		select {
		case <-ctx.Done():
			return cachedToken{}, ctx.Err()
		case <-call.done:
			return call.tok, call.err
		}
	}
	call := &tokenCall{done: make(chan struct{})}
	c.inflight[acc.RefreshToken] = call
	c.workMu.Unlock()

	call.tok, call.err = c.refreshToken(ctx, acc)
	c.workMu.Lock()
	delete(c.inflight, acc.RefreshToken)
	close(call.done)
	c.workMu.Unlock()
	if call.err != nil {
		return cachedToken{}, call.err
	}
	c.tokMu.Lock()
	c.tokens[acc.RefreshToken] = call.tok
	c.tokMu.Unlock()
	return call.tok, nil
}

func (c *Client) refreshToken(ctx context.Context, acc Account) (cachedToken, error) {
	response, err := c.requestToken(ctx, acc, graphScope)
	requestedGraph := true
	if err != nil && errors.Is(err, errScopeUnsupported) {
		response, err = c.requestToken(ctx, acc, "")
		requestedGraph = false
	}
	if err != nil {
		return cachedToken{}, err
	}
	protocol := protocolFromScope(response.Scope, requestedGraph)
	ttl := time.Duration(response.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	return cachedToken{
		access: response.AccessToken, protocol: protocol, expiresAt: time.Now().Add(ttl),
	}, nil
}

var errScopeUnsupported = errors.New("OAuth scope unsupported by refresh token")

func (c *Client) requestToken(ctx context.Context, acc Account, scope string) (oauthResponse, error) {
	form := url.Values{}
	form.Set("client_id", acc.ClientID)
	form.Set("refresh_token", acc.RefreshToken)
	form.Set("grant_type", "refresh_token")
	if scope != "" {
		form.Set("scope", scope)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return oauthResponse{}, fmt.Errorf("%w: token request: %v", ErrAuthTemporary, err)
	}
	defer resp.Body.Close()

	var token oauthResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return oauthResponse{}, fmt.Errorf("%w: token status=%d", ErrAuthTemporary, resp.StatusCode)
		}
		return oauthResponse{}, fmt.Errorf("token response: %w", err)
	}
	if token.Error != "" {
		if strings.Contains(strings.ToUpper(token.ErrorDesc), "AADSTS90023") {
			return oauthResponse{}, errScopeUnsupported
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 ||
			token.Error == "temporarily_unavailable" || token.Error == "server_error" {
			return oauthResponse{}, fmt.Errorf("%w: OAuth %s: %s", ErrAuthTemporary, token.Error, strings.TrimSpace(token.ErrorDesc))
		}
		return oauthResponse{}, fmt.Errorf("%w: OAuth %s: %s", ErrAuthFailed, token.Error, strings.TrimSpace(token.ErrorDesc))
	}
	if token.AccessToken == "" {
		return oauthResponse{}, fmt.Errorf("%w: empty access_token", ErrAuthFailed)
	}
	return token, nil
}

func protocolFromScope(scope string, requestedGraph bool) mailProtocol {
	for _, granted := range strings.Fields(strings.ToLower(scope)) {
		if granted == "https://outlook.office.com/imap.accessasuser.all" ||
			granted == "imap.accessasuser.all" {
			return protocolIMAP
		}
	}
	if !requestedGraph {
		// The no-scope retry is only used for legacy Outlook refresh tokens.
		return protocolIMAP
	}
	return protocolGraph
}

func (c *Client) invalidate(refreshToken string) {
	c.tokMu.Lock()
	delete(c.tokens, refreshToken)
	c.tokMu.Unlock()
}
