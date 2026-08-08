package grokreg

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

type geoInfo struct {
	Status      string  `json:"status"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Timezone    string  `json:"timezone"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Query       string  `json:"query"`
}

func lookupGeoIPViaRequest(in Input) *geoInfo {
	in.logf("正在查询出口 IP 地理位置...")

	transport := &http.Transport{}
	if strings.TrimSpace(in.Proxy) != "" {
		pu, perr := url.Parse(normalizeProxy(in.Proxy))
		if perr != nil {
			in.logf("代理解析失败，跳过地理位置对齐: %v", perr)
			return nil
		}
		transport.Proxy = http.ProxyURL(pu)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: transport}

	req, err := http.NewRequest(http.MethodGet,
		"http://ip-api.com/json/?fields=status,message,country,countryCode,region,city,timezone,lat,lon,query", nil)
	if err != nil {
		in.logf("GeoIP 查询失败，跳过地理位置对齐: %v", err)
		return nil
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		in.logf("GeoIP 查询失败，跳过地理位置对齐: %v", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var g geoInfo
	if err := json.Unmarshal(body, &g); err != nil || g.Status != "success" {
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		in.logf("GeoIP 查询失败，跳过地理位置对齐 (HTTP %d, resp=%q)", resp.StatusCode, snippet)
		return nil
	}
	in.logf("出口 IP=%s 位置=%s/%s 时区=%s", g.Query, g.CountryCode, g.City, g.Timezone)
	return &g
}

func applyGeo(page *rod.Page, g *geoInfo, in Input) {
	if g.Timezone != "" {
		_ = (proto.EmulationSetTimezoneOverride{TimezoneID: g.Timezone}).Call(page)
	}
	lat, lon, acc := g.Lat, g.Lon, 50.0
	_ = (proto.EmulationSetGeolocationOverride{Latitude: &lat, Longitude: &lon, Accuracy: &acc}).Call(page)

	locale, acceptLang := localeForCountry(g.CountryCode)
	_ = (proto.EmulationSetLocaleOverride{Locale: locale}).Call(page)
	in.logf("已对齐时区/坐标/语言: tz=%s locale=%s lang=%s", g.Timezone, locale, acceptLang)
}

func localeForCountry(cc string) (locale, acceptLang string) {
	switch strings.ToUpper(strings.TrimSpace(cc)) {
	case "US":
		return "en_US", "en-US,en;q=0.9"
	case "GB", "UK":
		return "en_GB", "en-GB,en;q=0.9"
	case "CA":
		return "en_CA", "en-CA,en;q=0.9,fr-CA;q=0.8"
	case "AU":
		return "en_AU", "en-AU,en;q=0.9"
	case "DE":
		return "de_DE", "de-DE,de;q=0.9,en;q=0.8"
	case "FR":
		return "fr_FR", "fr-FR,fr;q=0.9,en;q=0.8"
	case "ES":
		return "es_ES", "es-ES,es;q=0.9,en;q=0.8"
	case "IT":
		return "it_IT", "it-IT,it;q=0.9,en;q=0.8"
	case "NL":
		return "nl_NL", "nl-NL,nl;q=0.9,en;q=0.8"
	case "JP":
		return "ja_JP", "ja-JP,ja;q=0.9,en;q=0.8"
	case "KR":
		return "ko_KR", "ko-KR,ko;q=0.9,en;q=0.8"
	case "BR":
		return "pt_BR", "pt-BR,pt;q=0.9,en;q=0.8"
	case "RU":
		return "ru_RU", "ru-RU,ru;q=0.9"
	case "IN":
		return "en_IN", "en-IN,en;q=0.9,hi;q=0.8"
	case "SG":
		return "en_SG", "en-SG,en;q=0.9"
	default:
		return "en_US", "en-US,en;q=0.9"
	}
}
