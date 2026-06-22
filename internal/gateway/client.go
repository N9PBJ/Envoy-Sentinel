package gateway

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type TokenProvider func(context.Context) (string, error)

type Client struct {
	baseURL       string
	token         string
	tokenMu       sync.RWMutex
	tokenRefresh  sync.Mutex
	tokenProvider TokenProvider
	http          *http.Client
}

func (c *Client) SetTokenProvider(provider TokenProvider) {
	c.tokenMu.Lock()
	c.tokenProvider = provider
	c.tokenMu.Unlock()
}

func (c *Client) RefreshToken(ctx context.Context) error {
	c.tokenRefresh.Lock()
	defer c.tokenRefresh.Unlock()

	c.tokenMu.RLock()
	provider := c.tokenProvider
	c.tokenMu.RUnlock()
	if provider == nil {
		return fmt.Errorf("gateway token provider is not configured")
	}
	token, err := provider(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("gateway token provider returned an empty token")
	}
	c.tokenMu.Lock()
	c.token = strings.TrimSpace(token)
	c.tokenMu.Unlock()
	return nil
}

func (c *Client) bearerToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
}

type MeterDetails struct {
	EID             int    `json:"eid"`
	State           string `json:"state"`
	MeasurementType string `json:"measurementType"`
	PhaseMode       string `json:"phaseMode"`
	PhaseCount      int    `json:"phaseCount"`
	MeteringStatus  string `json:"meteringStatus"`
	StatusFlags     []any  `json:"statusFlags"`
}

type MeterReadings struct {
	Eid                 int       `json:"eid"`
	Timestamp           int       `json:"timestamp"`
	ActEnergyDlvd       float64   `json:"actEnergyDlvd"`
	ActEnergyRcvd       float64   `json:"actEnergyRcvd"`
	ApparentEnergy      float64   `json:"apparentEnergy"`
	ReactEnergyLagg     float64   `json:"reactEnergyLagg"`
	ReactEnergyLead     float64   `json:"reactEnergyLead"`
	InstantaneousDemand float64   `json:"instantaneousDemand"`
	ActivePower         float64   `json:"activePower"`
	ApparentPower       float64   `json:"apparentPower"`
	ReactivePower       float64   `json:"reactivePower"`
	PwrFactor           float64   `json:"pwrFactor"`
	Voltage             float64   `json:"voltage"`
	Current             float64   `json:"current"`
	Freq                float64   `json:"freq"`
	Channels            []Channel `json:"channels"`
}

type Channel struct {
	Eid                 int     `json:"eid"`
	Timestamp           int     `json:"timestamp"`
	ActEnergyDlvd       float64 `json:"actEnergyDlvd"`
	ActEnergyRcvd       float64 `json:"actEnergyRcvd"`
	ApparentEnergy      float64 `json:"apparentEnergy"`
	ReactEnergyLagg     float64 `json:"reactEnergyLagg"`
	ReactEnergyLead     float64 `json:"reactEnergyLead"`
	InstantaneousDemand float64 `json:"instantaneousDemand"`
	ActivePower         float64 `json:"activePower"`
	ApparentPower       float64 `json:"apparentPower"`
	ReactivePower       float64 `json:"reactivePower"`
	PwrFactor           float64 `json:"pwrFactor"`
	Voltage             float64 `json:"voltage"`
	Current             float64 `json:"current"`
	Freq                float64 `json:"freq"`
}

type (
	RawLiveData      map[string]any
	RawMeterDetails  []MeterDetails
	RawMeterReadings []MeterReadings

	PowerConsumptionData struct {
		CreatedAt  int                            `json:"createdAt"`
		ReportType string                         `json:"reportType"`
		Cumulative PowerConsumptionDataCumulative `json:"cumulative"`
		Lines      []PowerConsumptionDataLines    `json:"lines"`
	}
	PowerConsumptionDataCumulative struct {
		CurrW       float64 `json:"currW"`
		ActPower    float64 `json:"actPower"`
		ApprntPwr   float64 `json:"apprntPwr"`
		ReactPwr    float64 `json:"reactPwr"`
		WhDlvdCum   float64 `json:"whDlvdCum"`
		WhRcvdCum   float64 `json:"whRcvdCum"`
		VarhLagCum  float64 `json:"varhLagCum"`
		VarhLeadCum float64 `json:"varhLeadCum"`
		VahCum      float64 `json:"vahCum"`
		RmsVoltage  float64 `json:"rmsVoltage"`
		RmsCurrent  float64 `json:"rmsCurrent"`
		PwrFactor   float64 `json:"pwrFactor"`
		FreqHz      float64 `json:"freqHz"`
	}
	PowerConsumptionDataLines struct {
		CurrW       float64 `json:"currW"`
		ActPower    float64 `json:"actPower"`
		ApprntPwr   float64 `json:"apprntPwr"`
		ReactPwr    float64 `json:"reactPwr"`
		WhDlvdCum   float64 `json:"whDlvdCum"`
		WhRcvdCum   float64 `json:"whRcvdCum"`
		VarhLagCum  float64 `json:"varhLagCum"`
		VarhLeadCum float64 `json:"varhLeadCum"`
		VahCum      float64 `json:"vahCum"`
		RmsVoltage  float64 `json:"rmsVoltage"`
		RmsCurrent  float64 `json:"rmsCurrent"`
		PwrFactor   float64 `json:"pwrFactor"`
		FreqHz      float64 `json:"freqHz"`
	}

	ProductionMeterData struct {
		WattHoursToday     int `json:"wattHoursToday"`
		WattHoursSevenDays int `json:"wattHoursSevenDays"`
		WattHoursLifetime  int `json:"wattHoursLifetime"`
		WattsNow           int `json:"wattsNow"`
	}

	InverterProductionData struct {
		SerialNumber    string `json:"serialNumber"`
		LastReportDate  int    `json:"lastReportDate"`
		DevType         int    `json:"devType"`
		LastReportWatts int    `json:"lastReportWatts"`
		MaxReportWatts  int    `json:"maxReportWatts"`
	}

	EnergyData struct {
		Production  EnergyDataProduction  `json:"production"`
		Consumption EnergyDataConsumption `json:"consumption"`
	}

	EnergyDataPCU struct {
		WattHoursToday     int `json:"wattHoursToday"`
		WattHoursSevenDays int `json:"wattHoursSevenDays"`
		WattHoursLifetime  int `json:"wattHoursLifetime"`
		WattsNow           int `json:"wattsNow"`
	}
	EnergyDataRGM struct {
		WattHoursToday     int `json:"wattHoursToday"`
		WattHoursSevenDays int `json:"wattHoursSevenDays"`
		WattHoursLifetime  int `json:"wattHoursLifetime"`
		WattsNow           int `json:"wattsNow"`
	}
	EnergyDataEIM struct {
		WattHoursToday     int `json:"wattHoursToday"`
		WattHoursSevenDays int `json:"wattHoursSevenDays"`
		WattHoursLifetime  int `json:"wattHoursLifetime"`
		WattsNow           int `json:"wattsNow"`
	}
	EnergyDataProduction struct {
		PCU EnergyDataPCU `json:"pcu"`
		RGM EnergyDataRGM `json:"rgm"`
		EIM EnergyDataEIM `json:"eim"`
	}
	EnergyDataConsumptionEim struct {
		WattHoursToday     int `json:"wattHoursToday"`
		WattHoursSevenDays int `json:"wattHoursSevenDays"`
		WattHoursLifetime  int `json:"wattHoursLifetime"`
		WattsNow           int `json:"wattsNow"`
	}
	EnergyDataConsumption struct {
		ConsumptionEim EnergyDataConsumptionEim `json:"eim"`
	}

	GridReading struct {
		Channels []GridReadingChannels `json:"channels"`
	}
	GridReadingChannels struct {
		Phase         string  `json:"phase"`
		ActivePower   float64 `json:"activePower"`
		ReactivePower float64 `json:"reactivePower"`
		Voltage       float64 `json:"voltage"`
		Current       float64 `json:"current"`
		Freq          float64 `json:"freq"`
	}

	Tasks struct {
		TaskID    float64
		Timestamp float64
	}

	Sample struct {
		At                    time.Time
		SOC                   float64
		MainRelayState        int
		BatteryPowerW         float64
		GridPowerW            float64
		PVPowerW              float64
		LoadPowerW            float64
		SendDemandRspCtrl     *float64
		Tasks                 Tasks
		RawDiagnosticCounters map[string]float64
	}

	Info struct {
		Device struct {
			SerialNumber string `xml:"sn"`
			Software     string `xml:"software"`
		} `xml:"device"`
		WebTokens bool `xml:"web-tokens"`
	}
)

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

func (c *Client) Info(ctx context.Context, debug bool) (Info, error) {
	var raw Info
	if err := c.getXML(ctx, "/info", false, &raw, debug); err != nil {
		return Info{}, err
	}
	return raw, nil
}

func (c *Client) LiveData(ctx context.Context, debug bool) (RawLiveData, error) {
	var raw RawLiveData
	if err := c.getJSON(ctx, "/ivp/livedata/status", true, &raw, debug); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) MeterDetails(ctx context.Context, debug bool) (RawMeterDetails, error) {
	var raw RawMeterDetails
	if err := c.getJSON(ctx, "/ivp/meters", true, &raw, debug); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) MeterReadings(ctx context.Context, debug bool) (RawMeterReadings, error) {
	var raw RawMeterReadings
	if err := c.getJSON(ctx, "/ivp/meters/readings", true, &raw, debug); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) ProductionMeterData(ctx context.Context, debug bool) (ProductionMeterData, error) {
	var data ProductionMeterData
	if err := c.getJSON(ctx, "/api/v1/production", true, &data, debug); err != nil {
		return ProductionMeterData{}, err
	}
	return data, nil
}

func (c *Client) EnergyData(ctx context.Context, debug bool) (EnergyData, error) {
	var raw EnergyData
	if err := c.getJSON(ctx, "/ivp/pdm/energy", true, &raw, debug); err != nil {
		return EnergyData{}, err
	}
	return raw, nil
}

func (c *Client) InverterProductionData(ctx context.Context, debug bool) ([]InverterProductionData, error) {
	var data []InverterProductionData
	if err := c.getJSON(ctx, "/api/v1/production/inverters", true, &data, debug); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) PowerConsumptionData(ctx context.Context, debug bool) ([]PowerConsumptionData, error) {
	var data []PowerConsumptionData
	if err := c.getJSON(ctx, "/ivp/meters/reports/consumption", true, &data, debug); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) GridReadings(ctx context.Context, debug bool) ([]GridReading, error) {
	var data []GridReading
	if err := c.getJSON(ctx, "/ivp/meters/gridReading", true, &data, debug); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *Client) getXML(ctx context.Context, path string, authorized bool, dst *Info, debug bool) error {
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body: %v", err)
	}

	if debug {
		err = writeDebugFile(path, body, "xml")
		if err != nil {
			slog.Error("write XML debug file", "path", path, "error", err)
		}
	}

	err = xml.Unmarshal(body, dst)
	if err != nil {
		return fmt.Errorf("decode gateway %s response: %w", path, err)
	}
	return nil
}

func writeDebugFile(path string, body []byte, fType string) error {
	if err := os.MkdirAll("debug", 0o755); err != nil {
		return fmt.Errorf("create debug directory: %w", err)
	}
	debugPath := strings.ReplaceAll(path, "/", "_")
	filePath := fmt.Sprintf("./debug/%s-%s.%s", time.Now().UTC().Format("2006-01-02T15-04-05Z"), debugPath, fType)
	if err := os.WriteFile(filePath, body, 0o600); err != nil {
		return fmt.Errorf("write %s debug file: %w", fType, err)
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, path string, authorized bool, dst any, debug bool) error {
	var body []byte
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
		if err != nil {
			return err
		}
		if authorized {
			req.Header.Set("Authorization", "Bearer "+c.bearerToken())
		}
		req.Header.Set("Accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		body, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("error reading response body: %v", err)
		}

		if resp.StatusCode == http.StatusUnauthorized && authorized && attempt == 0 {
			if err := c.RefreshToken(ctx); err != nil {
				return fmt.Errorf("refresh gateway token after unauthorized response: %w", err)
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			message := strings.TrimSpace(string(body))
			if len(message) > 4096 {
				message = message[:4096]
			}
			return fmt.Errorf("gateway %s returned %s: %s", path, resp.Status, message)
		}
		break
	}

	if debug {
		err := writeDebugFile(path, body, "json")
		if err != nil {
			slog.Error("write JSON debug file", "path", path, "error", err)
		}
	}

	err := json.Unmarshal(body, dst)
	if err != nil {
		return fmt.Errorf("decode gateway %s response: %w", path, err)
	}

	return nil
}

func Normalize(raw RawLiveData, now time.Time) (Sample, error) {
	meters, _ := objectAt(raw, "meters")
	if meters == nil {
		return Sample{}, fmt.Errorf("live data missing meters object")
	}

	tasks, _ := objectAt(raw, "tasks")
	if tasks == nil {
		return Sample{}, fmt.Errorf("live data missing tasks object")
	}

	taskID, ok := numberAt(tasks, "task_id")
	if !ok {
		return Sample{}, fmt.Errorf("live data missing task_id in tasks object")
	}

	taskTimestamp, ok := numberAt(tasks, "timestamp")
	if !ok {
		return Sample{}, fmt.Errorf("live data missing timestamp in tasks object")
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
		Tasks: Tasks{
			TaskID:    taskID,
			Timestamp: taskTimestamp,
		},
	}
	if value, ok := numberAt(meters, "main_relay_state"); ok {
		sample.MainRelayState = int(value)
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

// objectAt retrieves an object at key as a map of JSON-compatible values.
func objectAt(parent map[string]any, key string) (map[string]any, bool) {
	value, ok := parent[key]
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	return obj, ok
}

// numberAt retrieves a numeric value at key as a float64.
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
