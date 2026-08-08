package mailfetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-sasl"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func tokenResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestVerifyDoesNotRetryPermanentOAuthError(t *testing.T) {
	client := New()
	calls := 0
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return tokenResponse(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"expired"}`), nil
	})
	err := client.Verify(context.Background(), Account{ClientID: "client", RefreshToken: "token"})
	if err == nil {
		t.Fatal("expected authentication error")
	}
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
}

func TestVerifyRetriesTemporaryOAuthError(t *testing.T) {
	client := New()
	tokenCalls := 0
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "graph.microsoft.com" {
			return tokenResponse(http.StatusOK, `{}`), nil
		}
		tokenCalls++
		if tokenCalls < 3 {
			return tokenResponse(http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`), nil
		}
		return tokenResponse(http.StatusOK, `{"access_token":"ok","expires_in":3600}`), nil
	})
	if err := client.Verify(context.Background(), Account{ClientID: "client", RefreshToken: "token"}); err != nil {
		t.Fatal(err)
	}
	if tokenCalls != 3 {
		t.Fatalf("token calls=%d want 3", tokenCalls)
	}
}

func TestTokenFallsBackToLegacyIMAPScope(t *testing.T) {
	client := New()
	calls := 0
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(req.Body)
		form, _ := url.ParseQuery(string(body))
		if calls == 1 {
			if form.Get("scope") != graphScope {
				t.Fatalf("scope=%q want Graph default", form.Get("scope"))
			}
			return tokenResponse(http.StatusBadRequest,
				`{"error":"invalid_request","error_description":"AADSTS90023: Public clients can't send scope"}`), nil
		}
		if form.Get("scope") != "" {
			t.Fatalf("fallback scope=%q want empty", form.Get("scope"))
		}
		return tokenResponse(http.StatusOK,
			`{"access_token":"imap-token","expires_in":3600,"scope":"IMAP.AccessAsUser.All SMTP.Send"}`), nil
	})
	token, err := client.accessToken(context.Background(), Account{ClientID: "client", RefreshToken: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if token.protocol != protocolIMAP {
		t.Fatalf("protocol=%v want IMAP", token.protocol)
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2", calls)
	}
}

func TestIMAPIDRoundTrip(t *testing.T) {
	id := encodeIMAPID("Junk Email:中文", 429)
	folder, uid, err := decodeIMAPID(id)
	if err != nil {
		t.Fatal(err)
	}
	if folder != "Junk Email:中文" || uid != 429 {
		t.Fatalf("folder=%q uid=%d", folder, uid)
	}
	for _, invalid := range []string{"graph-id", "imap::1", "imap:bad!:1", "imap:SU5CT1g:0"} {
		if _, _, err := decodeIMAPID(invalid); err == nil {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}

func TestParseMIMEMessage(t *testing.T) {
	raw := strings.NewReader("From: Sender <sender@example.com>\r\n" +
		"Subject: Test mail\r\n" +
		"Date: Fri, 25 Jul 2026 09:30:00 +0800\r\n" +
		"Content-Type: multipart/alternative; boundary=x\r\n\r\n" +
		"--x\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain body\r\n" +
		"--x\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<b>html body</b>\r\n--x--\r\n")
	message, err := parseMIMEMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if message.From != "sender@example.com" || message.FromName != "Sender" || message.Subject != "Test mail" {
		t.Fatalf("unexpected headers: %+v", message)
	}
	if !strings.Contains(message.Text, "plain body") || !strings.Contains(message.HTML, "html body") {
		t.Fatalf("unexpected bodies: text=%q html=%q", message.Text, message.HTML)
	}
}

type fakeIMAPSession struct {
	authenticated bool
	noops         int
	authErr       error
}

func (f *fakeIMAPSession) Authenticate(client sasl.Client) error {
	if f.authErr != nil {
		return f.authErr
	}
	mechanism, initial, err := client.Start()
	if err != nil {
		return err
	}
	if mechanism != "XOAUTH2" || string(initial) != "user=user@example.com\x01auth=Bearer access\x01\x01" {
		return io.ErrUnexpectedEOF
	}
	f.authenticated = true
	return nil
}
func (f *fakeIMAPSession) Noop() error                                       { f.noops++; return nil }
func (f *fakeIMAPSession) List(string, string, chan *imap.MailboxInfo) error { return nil }
func (f *fakeIMAPSession) Select(string, bool) (*imap.MailboxStatus, error) {
	return &imap.MailboxStatus{}, nil
}
func (f *fakeIMAPSession) UidSearch(*imap.SearchCriteria) ([]uint32, error) { return nil, nil }
func (f *fakeIMAPSession) UidFetch(*imap.SeqSet, []imap.FetchItem, chan *imap.Message) error {
	return nil
}
func (f *fakeIMAPSession) Logout() error { return nil }

func TestVerifyIMAPUsesXOAUTH2AndNoop(t *testing.T) {
	client := New()
	session := &fakeIMAPSession{}
	client.imapDial = func(context.Context) (imapSession, error) { return session, nil }
	if err := client.verifyIMAP(context.Background(), Account{Email: "user@example.com"}, "access"); err != nil {
		t.Fatal(err)
	}
	if !session.authenticated || session.noops != 1 {
		t.Fatalf("authenticated=%v noops=%d", session.authenticated, session.noops)
	}
}

func TestVerifyRetriesIMAPAuthRejectionWithFreshToken(t *testing.T) {
	client := New()
	tokenCalls := 0
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		tokenCalls++
		return tokenResponse(http.StatusOK,
			`{"access_token":"access","expires_in":3600,"scope":"IMAP.AccessAsUser.All"}`), nil
	})
	dials := 0
	client.imapDial = func(context.Context) (imapSession, error) {
		dials++
		if dials == 1 {
			return &fakeIMAPSession{authErr: errors.New("AUTHENTICATE failed")}, nil
		}
		return &fakeIMAPSession{}, nil
	}
	err := client.Verify(context.Background(), Account{
		Email: "user@example.com", ClientID: "client", RefreshToken: "refresh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dials != 2 || tokenCalls != 2 {
		t.Fatalf("dials=%d token calls=%d; want 2 each", dials, tokenCalls)
	}
}
