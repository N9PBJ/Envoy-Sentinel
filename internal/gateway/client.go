package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// type Info struct {
// 	Raw map[string]any
// }

type RawLiveData map[string]any

type Sample struct {
	At                    time.Time
	SOC                   float64
	BatteryPowerW         float64
	GridPowerW            float64
	PVPowerW              float64
	LoadPowerW            float64
	SendDemandRspCtrl     *float64
	RawDiagnosticCounters map[string]float64
}

type Info struct {
	Device struct {
		SerialNumber string `xml:"sn"`
		Software     string `xml:"software"`
	} `xml:"device"`
	WebTokens bool `xml:"web-tokens"`
}

func NewClient(baseURL, token string, allowInsecureTLS bool) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("gateway URL must include scheme and host: %q", baseURL)
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: allowInsecureTLS} //nolint:gosec // IQ Gateway commonly uses a self-signed local certificate.

	return &Client{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		token:   token,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: transport,
		},
	}, nil
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var raw Info
	if err := c.getXML(ctx, "/info", false, &raw); err != nil {
		return Info{}, err
	}
	return raw, nil
}

func (c *Client) LiveData(ctx context.Context) (RawLiveData, error) {
	var raw RawLiveData
	if err := c.getJSON(ctx, "/ivp/livedata/status", true, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) getXML(ctx context.Context, path string, authorized bool, dst *Info) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if authorized {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("gateway %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}

	if err := xml.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode gateway %s response: %w", path, err)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, authorized bool, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if authorized {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("gateway %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode gateway %s response: %w", path, err)
	}
	return nil
}

func Normalize(raw RawLiveData, now time.Time) (Sample, error) {
	meters, _ := objectAt(raw, "meters")
	if meters == nil {
		return Sample{}, fmt.Errorf("live data missing meters object")
	}

	at := now
	if epoch, ok := numberAt(meters, "last_update"); ok && epoch > 0 {
		at = time.Unix(int64(epoch), 0)
	}

	soc, ok := numberAt(meters, "enc_agg_soc")
	if !ok {
		soc, ok = numberAt(meters, "soc")
	}
	if !ok {
		return Sample{}, fmt.Errorf("live data missing SOC field")
	}

	sample := Sample{
		At:                    at,
		SOC:                   soc,
		RawDiagnosticCounters: map[string]float64{},
	}

	if storage, _ := objectAt(meters, "storage"); storage != nil {
		if value, ok := numberAt(storage, "agg_p_mw"); ok {
			sample.BatteryPowerW = milliwattsToWatts(value)
		}
	}
	if grid, _ := objectAt(meters, "grid"); grid != nil {
		if value, ok := numberAt(grid, "agg_p_mw"); ok {
			sample.GridPowerW = milliwattsToWatts(value)
		}
	}
	if pv, _ := objectAt(meters, "pv"); pv != nil {
		if value, ok := numberAt(pv, "agg_p_mw"); ok {
			sample.PVPowerW = milliwattsToWatts(value)
		}
	}
	if load, _ := objectAt(meters, "load"); load != nil {
		if value, ok := numberAt(load, "agg_p_mw"); ok {
			sample.LoadPowerW = milliwattsToWatts(value)
		}
	}

	for key, value := range findNumbers(map[string]any(raw), "sc_") {
		sample.RawDiagnosticCounters[key] = value
	}
	if value, ok := sample.RawDiagnosticCounters["sc_SendDemandRspCtrl"]; ok {
		v := value
		sample.SendDemandRspCtrl = &v
	}

	return sample, nil
}

func milliwattsToWatts(value float64) float64 {
	return value / 1000
}

func objectAt(parent map[string]any, key string) (map[string]any, bool) {
	value, ok := parent[key]
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	return obj, ok
}

func numberAt(parent map[string]any, key string) (float64, bool) {
	value, ok := parent[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func findNumbers(value any, keyPrefix string) map[string]float64 {
	found := map[string]float64{}
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, value := range typed {
				if strings.HasPrefix(key, keyPrefix) {
					if num, ok := asNumber(value); ok {
						found[key] = num
					}
				}
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		}
	}
	walk(value)
	return found
}

func asNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
