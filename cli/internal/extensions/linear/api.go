// Package linear lets an org's agents comment on Linear issues and get people's replies back.
package linear

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultAPI is Linear's GraphQL endpoint.
const DefaultAPI = "https://api.linear.app/graphql"

// Client calls Linear's GraphQL API with one API key.
type Client struct {
	Key string
	// URL is the GraphQL endpoint; empty means DefaultAPI.
	URL  string
	HTTP *http.Client
}

// Error is an error Linear returned.
type Error struct{ Message string }

func (e *Error) Error() string { return "linear: " + e.Message }

// Do runs one query or mutation and decodes its data into out.
func (c Client) Do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	u := c.URL
	if u == "" {
		u = DefaultAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", c.Key)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("linear: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return &Error{Message: strings.Join(msgs, "; ")}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("linear: %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// Workspace is who an API key belongs to, and its Linear workspace.
type Workspace struct {
	UserID   string
	UserName string
	OrgID    string
	OrgName  string
}

// Whoami returns the key's user and workspace.
func (c Client) Whoami(ctx context.Context) (Workspace, error) {
	var out struct {
		Viewer       struct{ ID, Name string }
		Organization struct{ ID, Name string }
	}
	if err := c.Do(ctx, `query { viewer { id name } organization { id name } }`, nil, &out); err != nil {
		return Workspace{}, err
	}
	return Workspace{UserID: out.Viewer.ID, UserName: out.Viewer.Name, OrgID: out.Organization.ID, OrgName: out.Organization.Name}, nil
}

// Issue is a Linear issue.
type Issue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

// Issue looks up an issue by its identifier (ENG-123) or ID.
func (c Client) Issue(ctx context.Context, id string) (Issue, error) {
	var out struct{ Issue *Issue }
	if err := c.Do(ctx, `query($id: String!) { issue(id: $id) { id identifier title url } }`, map[string]any{"id": id}, &out); err != nil {
		return Issue{}, err
	}
	if out.Issue == nil {
		return Issue{}, &Error{Message: "no issue " + id}
	}
	return *out.Issue, nil
}

// Comment is one comment on an issue.
type Comment struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	User      string    `json:"user,omitempty"`
	URL       string    `json:"url,omitempty"`
}

// Comment posts a comment on an issue.
func (c Client) Comment(ctx context.Context, issueID, body string) (Comment, error) {
	var out struct {
		CommentCreate struct {
			Success bool
			Comment struct {
				ID, URL   string
				CreatedAt time.Time
			}
		}
	}
	err := c.Do(ctx, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success comment { id url createdAt } } }`,
		map[string]any{"input": map[string]any{"issueId": issueID, "body": body}}, &out)
	if err != nil {
		return Comment{}, err
	}
	if !out.CommentCreate.Success {
		return Comment{}, &Error{Message: "the comment was not created"}
	}
	cm := out.CommentCreate.Comment
	return Comment{ID: cm.ID, Body: body, CreatedAt: cm.CreatedAt, URL: cm.URL}, nil
}

// Comments returns an issue's latest comments, oldest first.
func (c Client) Comments(ctx context.Context, issueID string, limit int) ([]Comment, error) {
	var out struct {
		Issue *struct {
			Comments struct {
				Nodes []struct {
					ID, Body, URL string
					CreatedAt     time.Time
					User          *struct{ Name, DisplayName string }
				}
			}
		}
	}
	err := c.Do(ctx, `query($id: String!, $n: Int!) { issue(id: $id) { comments(last: $n) { nodes { id body url createdAt user { name displayName } } } } }`,
		map[string]any{"id": issueID, "n": limit}, &out)
	if err != nil {
		return nil, err
	}
	if out.Issue == nil {
		return nil, &Error{Message: "no issue " + issueID}
	}
	list := make([]Comment, 0, len(out.Issue.Comments.Nodes))
	for _, n := range out.Issue.Comments.Nodes {
		cm := Comment{ID: n.ID, Body: n.Body, CreatedAt: n.CreatedAt, URL: n.URL}
		if n.User != nil {
			cm.User = n.User.DisplayName
			if cm.User == "" {
				cm.User = n.User.Name
			}
		}
		list = append(list, cm)
	}
	sortOldestFirst(list)
	return list, nil
}

func sortOldestFirst(list []Comment) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].CreatedAt.Before(list[j-1].CreatedAt); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// CreateWebhook asks Linear to send every comment in the workspace's public teams to url, signed with
// secret. It needs an admin's API key.
func (c Client) CreateWebhook(ctx context.Context, url, secret string) (string, error) {
	var out struct {
		WebhookCreate struct {
			Success bool
			Webhook struct{ ID string }
		}
	}
	err := c.Do(ctx, `mutation($input: WebhookCreateInput!) { webhookCreate(input: $input) { success webhook { id } } }`,
		map[string]any{"input": map[string]any{"url": url, "secret": secret, "label": "Context Drop", "allPublicTeams": true, "resourceTypes": []string{"Comment"}}}, &out)
	if err != nil {
		return "", err
	}
	if !out.WebhookCreate.Success || out.WebhookCreate.Webhook.ID == "" {
		return "", &Error{Message: "the webhook was not created"}
	}
	return out.WebhookCreate.Webhook.ID, nil
}

// DeleteWebhook removes a webhook.
func (c Client) DeleteWebhook(ctx context.Context, id string) error {
	return c.Do(ctx, `mutation($id: String!) { webhookDelete(id: $id) { success } }`, map[string]any{"id": id}, nil)
}

// MaxSkew is how old a webhook may be; Linear recommends a minute.
const MaxSkew = time.Minute

// Verify checks a webhook's Linear-Signature (hex HMAC-SHA256 of the raw body) and its timestamp.
func Verify(secret string, h http.Header, body []byte, now time.Time) error {
	if secret == "" {
		return errors.New("no webhook secret")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(h.Get("Linear-Signature"))) {
		return errors.New("bad signature")
	}
	var ts struct {
		WebhookTimestamp int64 `json:"webhookTimestamp"`
	}
	if err := json.Unmarshal(body, &ts); err != nil || ts.WebhookTimestamp == 0 {
		return errors.New("missing webhook timestamp")
	}
	if d := now.Sub(time.UnixMilli(ts.WebhookTimestamp)); d > MaxSkew || d < -MaxSkew {
		return errors.New("webhook timestamp is too old")
	}
	return nil
}

// Sign makes the signature header Verify accepts, for tests.
func Sign(secret string, body []byte) http.Header {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	h := http.Header{}
	h.Set("Linear-Signature", hex.EncodeToString(mac.Sum(nil)))
	return h
}
