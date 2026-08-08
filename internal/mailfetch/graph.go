package mailfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const graphBase = "https://graph.microsoft.com/v1.0"

var (
	errTokenUnauthorized = errors.New("mail access token unauthorized")
	graphFolders         = []string{"Inbox", "JunkEmail"}
)

type graphMessage struct {
	ID               string `json:"id"`
	Subject          string `json:"subject"`
	ReceivedDateTime string `json:"receivedDateTime"`
	Body             struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	From struct {
		EmailAddress struct {
			Address string `json:"address"`
			Name    string `json:"name"`
		} `json:"emailAddress"`
	} `json:"from"`
}

func (c *Client) verifyGraph(ctx context.Context, accessToken string) error {
	u := graphBase + "/me/mailFolders/inbox?$select=id"
	resp, err := c.graphGet(ctx, accessToken, u)
	if err != nil {
		return fmt.Errorf("%w: Graph request: %v", ErrAuthTemporary, err)
	}
	defer resp.Body.Close()
	return graphStatusError(resp.StatusCode)
}

func (c *Client) listGraphMessages(ctx context.Context, accessToken string, limit int) ([]Message, error) {
	type folderResult struct {
		messages []graphMessage
		err      error
	}
	results := make([]folderResult, len(graphFolders))
	var wg sync.WaitGroup
	for i, folder := range graphFolders {
		wg.Add(1)
		go func(index int, name string) {
			defer wg.Done()
			results[index].messages, results[index].err = c.listGraphFolder(ctx, accessToken, name, limit)
		}(i, folder)
	}
	wg.Wait()

	var output []Message
	var firstErr error
	for _, result := range results {
		if result.err != nil {
			if errors.Is(result.err, errTokenUnauthorized) {
				return nil, result.err
			}
			if firstErr == nil {
				firstErr = result.err
			}
			continue
		}
		for _, item := range result.messages {
			received, _ := time.Parse(time.RFC3339, item.ReceivedDateTime)
			output = append(output, Message{
				ID: item.ID, From: item.From.EmailAddress.Address,
				FromName: item.From.EmailAddress.Name, Subject: item.Subject, ReceivedAt: received,
			})
		}
	}
	if len(output) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return sortAndLimit(output, limit), nil
}

func (c *Client) listGraphFolder(ctx context.Context, accessToken, folder string, top int) ([]graphMessage, error) {
	query := url.Values{}
	query.Set("$top", fmt.Sprintf("%d", top))
	query.Set("$orderby", "receivedDateTime desc")
	query.Set("$select", "id,subject,receivedDateTime,from")
	u := fmt.Sprintf("%s/me/mailFolders/%s/messages?%s", graphBase, folder, query.Encode())
	resp, err := c.graphGet(ctx, accessToken, u)
	if err != nil {
		return nil, fmt.Errorf("%w: Graph request: %v", ErrAuthTemporary, err)
	}
	defer resp.Body.Close()
	if err := graphStatusError(resp.StatusCode); err != nil {
		return nil, err
	}
	var data struct {
		Value []graphMessage `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data.Value, nil
}

func (c *Client) getGraphMessage(ctx context.Context, accessToken, messageID string) (Message, error) {
	query := url.Values{}
	query.Set("$select", "id,subject,body,receivedDateTime,from")
	u := fmt.Sprintf("%s/me/messages/%s?%s", graphBase, url.PathEscape(messageID), query.Encode())
	resp, err := c.graphGet(ctx, accessToken, u)
	if err != nil {
		return Message{}, fmt.Errorf("%w: Graph request: %v", ErrAuthTemporary, err)
	}
	defer resp.Body.Close()
	if err := graphStatusError(resp.StatusCode); err != nil {
		return Message{}, err
	}
	var item graphMessage
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return Message{}, err
	}
	htmlBody, textBody := "", ""
	if strings.EqualFold(item.Body.ContentType, "html") {
		htmlBody = item.Body.Content
		textBody = stripHTML(item.Body.Content)
	} else {
		textBody = item.Body.Content
	}
	received, _ := time.Parse(time.RFC3339, item.ReceivedDateTime)
	return Message{
		ID: item.ID, From: item.From.EmailAddress.Address, FromName: item.From.EmailAddress.Name,
		Subject: item.Subject, ReceivedAt: received, HTML: htmlBody, Text: textBody,
	}, nil
}

func (c *Client) graphGet(ctx context.Context, accessToken, endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

func graphStatusError(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return errTokenUnauthorized
	case status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("%w: Graph status=%d", ErrAuthTemporary, status)
	case status >= 300:
		return fmt.Errorf("Graph status=%d", status)
	default:
		return nil
	}
}

var (
	htmlTagRe  = regexp.MustCompile(`<[^>]*>`)
	htmlEntity = regexp.MustCompile(`&[a-zA-Z0-9#]+;`)
	whitespace = regexp.MustCompile(`\s+`)
)

func stripHTML(value string) string {
	value = htmlTagRe.ReplaceAllString(value, " ")
	value = htmlEntity.ReplaceAllString(value, " ")
	return strings.TrimSpace(whitespace.ReplaceAllString(value, " "))
}
