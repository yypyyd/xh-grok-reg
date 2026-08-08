package mailfetch

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	_ "github.com/emersion/go-message/charset"
	messagemail "github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
)

const outlookIMAPAddress = "outlook.office365.com:993"

var errIMAPAuthRejected = errors.New("IMAP XOAUTH2 rejected")

type imapSession interface {
	Authenticate(sasl.Client) error
	Noop() error
	List(string, string, chan *imap.MailboxInfo) error
	Select(string, bool) (*imap.MailboxStatus, error)
	UidSearch(*imap.SearchCriteria) ([]uint32, error)
	UidFetch(*imap.SeqSet, []imap.FetchItem, chan *imap.Message) error
	Logout() error
}

type imapDialFunc func(context.Context) (imapSession, error)

type xoauth2Client struct {
	username    string
	accessToken string
	started     bool
}

func (x *xoauth2Client) Start() (string, []byte, error) {
	x.started = true
	response := "user=" + x.username + "\x01auth=Bearer " + x.accessToken + "\x01\x01"
	return "XOAUTH2", []byte(response), nil
}

func (x *xoauth2Client) Next(_ []byte) ([]byte, error) {
	if !x.started {
		return nil, errors.New("XOAUTH2 exchange not started")
	}
	// Outlook returns a JSON challenge before the final SASL failure. An empty
	// response completes that exchange without logging the challenge or token.
	return []byte{}, nil
}

func dialOutlookIMAP(ctx context.Context) (imapSession, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	tlsDialer := &tls.Dialer{NetDialer: dialer, Config: &tls.Config{
		MinVersion: tls.VersionTLS12, ServerName: "outlook.office365.com",
	}}
	conn, err := tlsDialer.DialContext(ctx, "tcp", outlookIMAPAddress)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(20 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, err
	}
	session, err := client.New(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return session, nil
}

func (c *Client) openIMAP(ctx context.Context, acc Account, accessToken string) (imapSession, error) {
	session, err := c.imapDial(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: IMAP connect: %v", ErrAuthTemporary, err)
	}
	if err := session.Authenticate(&xoauth2Client{username: acc.Email, accessToken: accessToken}); err != nil {
		session.Logout()
		if isTemporaryIMAPError(err) {
			return nil, fmt.Errorf("%w: IMAP authenticate transport failure", ErrAuthTemporary)
		}
		return nil, fmt.Errorf("%w: %w", ErrAuthFailed, errIMAPAuthRejected)
	}
	return session, nil
}

func (c *Client) verifyIMAP(ctx context.Context, acc Account, accessToken string) error {
	session, err := c.openIMAP(ctx, acc, accessToken)
	if err != nil {
		return err
	}
	defer session.Logout()
	if err := session.Noop(); err != nil {
		return fmt.Errorf("%w: IMAP NOOP: %v", ErrAuthTemporary, err)
	}
	return nil
}

func (c *Client) listIMAPMessages(ctx context.Context, acc Account, accessToken string, limit int) ([]Message, error) {
	session, err := c.openIMAP(ctx, acc, accessToken)
	if err != nil {
		return nil, err
	}
	defer session.Logout()

	folders, err := discoverIMAPFolders(session)
	if err != nil {
		return nil, fmt.Errorf("%w: IMAP LIST: %v", ErrAuthTemporary, err)
	}
	var output []Message
	var firstErr error
	for _, folder := range folders {
		messages, folderErr := listIMAPFolder(session, folder, limit)
		if folderErr != nil {
			if firstErr == nil {
				firstErr = folderErr
			}
			continue
		}
		output = append(output, messages...)
	}
	if len(output) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return sortAndLimit(output, limit), nil
}

func discoverIMAPFolders(session imapSession) ([]string, error) {
	mailboxes := make(chan *imap.MailboxInfo, 16)
	done := make(chan error, 1)
	go func() {
		done <- session.List("", "*", mailboxes)
	}()
	junk := ""
	known := map[string]string{}
	for mailbox := range mailboxes {
		known[strings.ToLower(mailbox.Name)] = mailbox.Name
		for _, attribute := range mailbox.Attributes {
			if strings.EqualFold(attribute, "\\Junk") {
				junk = mailbox.Name
			}
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	folders := []string{"INBOX"}
	if junk == "" {
		for _, candidate := range []string{"Junk", "Junk Email"} {
			if actual, ok := known[strings.ToLower(candidate)]; ok {
				junk = actual
				break
			}
		}
	}
	if junk != "" && !strings.EqualFold(junk, "INBOX") {
		folders = append(folders, junk)
	}
	return folders, nil
}

func listIMAPFolder(session imapSession, folder string, limit int) ([]Message, error) {
	status, err := session.Select(folder, true)
	if err != nil {
		return nil, err
	}
	if status.Messages == 0 {
		return nil, nil
	}
	uids, err := session.UidSearch(&imap.SearchCriteria{})
	if err != nil {
		return nil, err
	}
	if len(uids) > limit {
		uids = uids[len(uids)-limit:]
	}
	if len(uids) == 0 {
		return nil, nil
	}
	set := new(imap.SeqSet)
	set.AddNum(uids...)
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate}
	messages := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() {
		done <- session.UidFetch(set, items, messages)
	}()
	output := make([]Message, 0, len(uids))
	for item := range messages {
		if item == nil || item.Envelope == nil {
			continue
		}
		from, fromName := envelopeSender(item.Envelope)
		received := item.InternalDate
		if received.IsZero() {
			received = item.Envelope.Date
		}
		output = append(output, Message{
			ID: encodeIMAPID(folder, item.Uid), From: from, FromName: fromName,
			Subject: item.Envelope.Subject, ReceivedAt: received,
		})
	}
	if err := <-done; err != nil {
		return nil, err
	}
	return output, nil
}

func (c *Client) getIMAPMessage(ctx context.Context, acc Account, accessToken, messageID string) (Message, error) {
	folder, uid, err := decodeIMAPID(messageID)
	if err != nil {
		return Message{}, err
	}
	session, err := c.openIMAP(ctx, acc, accessToken)
	if err != nil {
		return Message{}, err
	}
	defer session.Logout()
	if _, err := session.Select(folder, true); err != nil {
		return Message{}, err
	}
	set := new(imap.SeqSet)
	set.AddNum(uid)
	section := &imap.BodySectionName{Peek: true}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- session.UidFetch(set, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, messages)
	}()
	var raw io.Reader
	for item := range messages {
		if item != nil && item.Uid == uid {
			raw = item.GetBody(section)
			if raw != nil {
				break
			}
		}
	}
	if err := <-done; err != nil {
		return Message{}, err
	}
	if raw == nil {
		return Message{}, errors.New("IMAP message not found")
	}
	message, err := parseMIMEMessage(raw)
	if err != nil {
		return Message{}, err
	}
	message.ID = messageID
	return message, nil
}

func envelopeSender(envelope *imap.Envelope) (string, string) {
	if envelope == nil || len(envelope.From) == 0 || envelope.From[0] == nil {
		return "", ""
	}
	address := envelope.From[0]
	return address.MailboxName + "@" + address.HostName, address.PersonalName
}

func encodeIMAPID(folder string, uid uint32) string {
	encodedFolder := base64.RawURLEncoding.EncodeToString([]byte(folder))
	return fmt.Sprintf("imap:%s:%d", encodedFolder, uid)
}

func decodeIMAPID(value string) (string, uint32, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "imap" {
		return "", 0, errors.New("invalid IMAP message ID")
	}
	folder, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(folder) == 0 {
		return "", 0, errors.New("invalid IMAP folder ID")
	}
	uid64, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil || uid64 == 0 {
		return "", 0, errors.New("invalid IMAP UID")
	}
	return string(folder), uint32(uid64), nil
}

func parseMIMEMessage(raw io.Reader) (Message, error) {
	reader, err := messagemail.CreateReader(raw)
	if err != nil {
		return Message{}, err
	}
	result := Message{}
	result.Subject, _ = reader.Header.Subject()
	result.ReceivedAt, _ = reader.Header.Date()
	if addresses, addressErr := reader.Header.AddressList("From"); addressErr == nil && len(addresses) > 0 {
		result.From = addresses[0].Address
		result.FromName = addresses[0].Name
	}
	for {
		part, nextErr := reader.NextPart()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return Message{}, nextErr
		}
		inline, ok := part.Header.(*messagemail.InlineHeader)
		if !ok {
			continue
		}
		mediaType, _, typeErr := inline.ContentType()
		if typeErr != nil {
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(part.Body, 8<<20))
		if readErr != nil {
			return Message{}, readErr
		}
		switch strings.ToLower(mediaType) {
		case "text/plain":
			if result.Text == "" {
				result.Text = string(body)
			}
		case "text/html":
			if result.HTML == "" {
				result.HTML = string(body)
			}
		}
	}
	if result.Text == "" && result.HTML != "" {
		result.Text = stripHTML(result.HTML)
	}
	return result, nil
}

func isTemporaryIMAPError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	messageText := strings.ToLower(err.Error())
	for _, fragment := range []string{"eof", "timeout", "connection reset", "broken pipe", "unavailable"} {
		if strings.Contains(messageText, fragment) {
			return true
		}
	}
	return false
}
