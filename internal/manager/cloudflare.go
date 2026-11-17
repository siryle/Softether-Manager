package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type cfResponse struct {
	Success  bool     `json:"success"`
	Errors   []cfErr  `json:"errors"`
	Messages []string `json:"messages"`
}

type cfErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e cfErr) String() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

func cfFindZone(ctx context.Context, token, domain string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid domain: %s", domain)
	}

	// Try sub-zones from longest suffix to shortest (e.g. sub.example.com -> example.com)
	for i := 0; i <= len(parts)-2; i++ {
		candidate := strings.Join(parts[i:], ".")
		url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones?name=%s&status=active", candidate)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		var res struct {
			Success bool `json:"success"`
			Result  []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"result"`
			Errors []cfErr `json:"errors"`
		}
		if err := json.Unmarshal(body, &res); err == nil && res.Success && len(res.Result) > 0 {
			return res.Result[0].ID, nil
		}
	}

	return "", fmt.Errorf("could not find Cloudflare zone for domain %s", domain)
}

func cfCreateTXT(ctx context.Context, token, zoneID, name, content string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", zoneID)

	payload := map[string]any{
		"type":    "TXT",
		"name":    name,
		"content": content,
		"ttl":     120,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var res struct {
		Success bool `json:"success"`
		Result  struct {
			ID string `json:"id"`
		} `json:"result"`
		Errors []cfErr `json:"errors"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return "", err
	}
	if !res.Success {
		var errMsgs []string
		for _, e := range res.Errors {
			errMsgs = append(errMsgs, e.String())
		}
		return "", fmt.Errorf("Cloudflare API error: %s", strings.Join(errMsgs, "; "))
	}

	return res.Result.ID, nil
}

func cfDeleteTXT(ctx context.Context, token, zoneID, recordID string) error {
	client := &http.Client{Timeout: 15 * time.Second}
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", zoneID, recordID)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var res cfResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return err
	}
	if !res.Success {
		var errMsgs []string
		for _, e := range res.Errors {
			errMsgs = append(errMsgs, e.String())
		}
		return fmt.Errorf("Cloudflare API delete error: %s", strings.Join(errMsgs, "; "))
	}

	return nil
}
