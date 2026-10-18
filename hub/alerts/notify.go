// Package alerts evaluates alert rules against live host and container
// state and delivers notifications.
package alerts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ChannelConfig is the union of all channel settings; which fields are used
// depends on the channel type.
type ChannelConfig struct {
	// ntfy, webhook
	URL string `json:"url,omitempty"`
	// ntfy access token
	Token string `json:"token,omitempty"`
	// ntfy priority 1-5
	Priority int `json:"priority,omitempty"`
	// discord
	WebhookURL string `json:"webhook_url,omitempty"`
	// telegram
	BotToken string `json:"bot_token,omitempty"`
	ChatID   string `json:"chat_id,omitempty"`
	// webhook HMAC secret
	Secret string `json:"secret,omitempty"`
	// email
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	// email transport security: starttls (default), tls, none
	Security string `json:"security,omitempty"`
}

// SecretFields lists config fields that are never returned to the browser.
var SecretFields = []string{"token", "webhook_url", "bot_token", "secret", "password"}

// Masked returns a copy without secrets and the names of secrets that are set.
func (c ChannelConfig) Masked() (ChannelConfig, []string) {
	var set []string
	mask := func(v *string, name string) {
		if *v != "" {
			set = append(set, name)
			*v = ""
		}
	}
	mask(&c.Token, "token")
	mask(&c.WebhookURL, "webhook_url")
	mask(&c.BotToken, "bot_token")
	mask(&c.Secret, "secret")
	mask(&c.Password, "password")
	return c, set
}

// MergeSecrets keeps previous secret values for fields left empty on update.
func (c *ChannelConfig) MergeSecrets(prev ChannelConfig) {
	keep := func(v *string, old string) {
		if *v == "" {
			*v = old
		}
	}
	keep(&c.Token, prev.Token)
	keep(&c.WebhookURL, prev.WebhookURL)
	keep(&c.BotToken, prev.BotToken)
	keep(&c.Secret, prev.Secret)
	keep(&c.Password, prev.Password)
}

func httpURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("must be an http(s) URL")
	}
	return nil
}

// Validate checks the configuration of a channel type.
func (c ChannelConfig) Validate(typ string) error {
	switch typ {
	case "ntfy":
		if err := httpURL(c.URL); err != nil {
			return fmt.Errorf("url %w (e.g. https://ntfy.sh/my-topic)", err)
		}
	case "discord":
		if err := httpURL(c.WebhookURL); err != nil {
			return fmt.Errorf("webhook_url %w", err)
		}
	case "telegram":
		if c.BotToken == "" || c.ChatID == "" {
			return errors.New("bot_token and chat_id are required")
		}
	case "webhook":
		if err := httpURL(c.URL); err != nil {
			return fmt.Errorf("url %w", err)
		}
	case "email":
		if c.Host == "" || c.From == "" || c.To == "" {
			return errors.New("host, from and to are required")
		}
		// Header injection guard: addresses end up in raw SMTP headers.
		if strings.ContainsAny(c.From+c.To+c.Host, "\r\n") {
			return errors.New("invalid characters in email settings")
		}
		switch c.Security {
		case "", "starttls", "tls", "none":
		default:
			return errors.New("security must be starttls, tls or none")
		}
	default:
		return fmt.Errorf("unknown channel type %q", typ)
	}
	return nil
}

// Notification is what gets delivered.
type Notification struct {
	Resolved bool      `json:"resolved"`
	Rule     string    `json:"rule"`
	RuleType string    `json:"rule_type"`
	Host     string    `json:"host"`
	Subject  string    `json:"subject,omitempty"`
	Message  string    `json:"message"`
	Value    float64   `json:"value"`
	Time     time.Time `json:"time"`
}

func (n Notification) Title() string {
	if n.Resolved {
		return "✅ Rozwiązane: " + n.Rule + " · " + n.Host
	}
	return "🔴 " + n.Rule + " · " + n.Host
}

var httpClient = &http.Client{
	Timeout: 15 * time.Second,
	// Notifications go to the configured endpoint only.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func post(ctx context.Context, rawURL, contentType string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "NexOS-alerts")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Send delivers n through a channel of the given type.
func Send(ctx context.Context, typ string, c ChannelConfig, n Notification) error {
	switch typ {
	case "ntfy":
		h := map[string]string{"Title": n.Title(), "Tags": "rotating_light"}
		if n.Resolved {
			h["Tags"] = "white_check_mark"
		}
		if c.Priority >= 1 && c.Priority <= 5 {
			h["Priority"] = strconv.Itoa(c.Priority)
		}
		if c.Token != "" {
			h["Authorization"] = "Bearer " + c.Token
		}
		// ntfy titles travel in a header: keep them ASCII-safe via RFC 2047.
		h["Title"] = mime.QEncoding.Encode("utf-8", h["Title"])
		return post(ctx, c.URL, "text/plain; charset=utf-8", []byte(n.Message), h)
	case "discord":
		color := 0xff5a4e
		if n.Resolved {
			color = 0x4ade80
		}
		body, _ := json.Marshal(map[string]any{
			"username": "NexOS",
			"embeds": []map[string]any{{
				"title": n.Title(), "description": n.Message, "color": color, "timestamp": n.Time.Format(time.RFC3339),
			}},
		})
		return post(ctx, c.WebhookURL, "application/json", body, nil)
	case "telegram":
		text := "<b>" + htmlEscape(n.Title()) + "</b>\n" + htmlEscape(n.Message)
		body, _ := json.Marshal(map[string]any{"chat_id": c.ChatID, "text": text, "parse_mode": "HTML", "disable_web_page_preview": true})
		return post(ctx, "https://api.telegram.org/bot"+url.PathEscape(c.BotToken)+"/sendMessage", "application/json", body, nil)
	case "webhook":
		body, _ := json.Marshal(n)
		h := map[string]string{}
		if c.Secret != "" {
			mac := hmac.New(sha256.New, []byte(c.Secret))
			mac.Write(body)
			h["X-NexOS-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
		}
		return post(ctx, c.URL, "application/json", body, h)
	case "email":
		return sendEmail(ctx, c, n)
	}
	return fmt.Errorf("unknown channel type %q", typ)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func sendEmail(ctx context.Context, c ChannelConfig, n Notification) error {
	port := c.Port
	if port == 0 {
		port = 587
		if c.Security == "tls" {
			port = 465
		}
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tlsCfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	if c.Security == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer cl.Close()
	if c.Security == "" || c.Security == "starttls" {
		if err := cl.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if c.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return err
	}
	recipients := strings.FieldsFunc(c.To, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
	for _, rcpt := range recipients {
		if err := cl.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	msg := "From: " + c.From + "\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", n.Title()) + "\r\n" +
		"Date: " + n.Time.Format(time.RFC1123Z) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(n.Message, "\n", "\r\n") + "\r\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}
