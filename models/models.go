package models

import (
	"encoding/json"
	"strings"
)

// Playback holds playback identifiers for Vidara, Streamtape, StreamHG, HDHub/HubCloud, or a direct stream URL.
type Playback struct {
	VA       string `json:"va,omitempty"`
	ST       string `json:"st,omitempty"`
	SH       string `json:"sh,omitempty"`
	HDHub    string `json:"hdhub,omitempty"`
	HubCloud string `json:"hubcloud,omitempty"`
	HubHD    string `json:"hubhd,omitempty"`
	URL      string `json:"url,omitempty"`
	Play     string `json:"play,omitempty"`
}

// UnmarshalJSON handles both object and string representations of playback.
func (p *Playback) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		str = strings.TrimSpace(str)
		if strings.HasPrefix(str, "http://") || strings.HasPrefix(str, "https://") || strings.Contains(str, "://") {
			p.URL = str
			p.Play = str
		} else {
			p.VA = str
		}
		return nil
	}

	type Alias Playback
	var aux struct {
		Alias
		DirectURL string `json:"url"`
		PlayVal   string `json:"play"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}

	p.VA = aux.VA
	p.ST = aux.ST
	p.SH = aux.SH
	p.HDHub = aux.HDHub
	p.HubCloud = aux.HubCloud
	p.HubHD = aux.HubHD
	if p.HDHub == "" && aux.HubHD != "" {
		p.HDHub = aux.HubHD
	}
	if p.HDHub == "" && aux.HubCloud != "" {
		p.HDHub = aux.HubCloud
	}
	p.URL = aux.URL
	p.Play = aux.Play

	if p.URL == "" && aux.DirectURL != "" {
		p.URL = aux.DirectURL
	}
	if p.URL == "" && (strings.HasPrefix(aux.PlayVal, "http://") || strings.HasPrefix(aux.PlayVal, "https://") || strings.Contains(aux.PlayVal, "://")) {
		p.URL = aux.PlayVal
	}
	if p.Play == "" && aux.PlayVal != "" {
		p.Play = aux.PlayVal
	}

	return nil
}

// EpisodeItem represents an episode in a TV season.
type EpisodeItem struct {
	Episode int      `json:"episode"`
	Play    Playback `json:"play"`
}

// MediaItem represents an item in the local Vidara database.
type MediaItem struct {
	ID       int           `json:"id"`
	Title    string        `json:"title"`
	Type     string        `json:"type"` // "movie" or "tv"
	Quality  string        `json:"quality,omitempty"`
	Season   int           `json:"season,omitempty"`
	Episodes []EpisodeItem `json:"episodes,omitempty"`
	Play     *Playback     `json:"play,omitempty"`
	VA       string        `json:"va,omitempty"`
	ST       string        `json:"st,omitempty"`
	SH       string        `json:"sh,omitempty"`
	HDHub    string        `json:"hdhub,omitempty"`
	HubCloud string        `json:"hubcloud,omitempty"`
	HubHD    string        `json:"hubhd,omitempty"`
	URL      string        `json:"url,omitempty"`
}

// ResolutionResult represents the final response from the 3-Tier resolver.
type ResolutionResult struct {
	Success bool              `json:"success"`
	TmdbID  int               `json:"tmdb_id"`
	Type    string            `json:"type,omitempty"`
	Tier    int               `json:"tier,omitempty"`
	Source  string            `json:"source,omitempty"`
	Title   string            `json:"title,omitempty"`
	Name    string            `json:"name,omitempty"`
	Season  *int              `json:"season,omitempty"`
	Episode *int              `json:"episode,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Expiry     *int64            `json:"expiry,omitempty"`
	Play       *Playback         `json:"play,omitempty"`
	Direct     bool              `json:"direct,omitempty"`
	StreamType string            `json:"stream_type,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// AudioTrack represents dynamic audio track extracted from HLS / M3U.
type AudioTrack struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language"`
	Default  bool   `json:"default"`
	URL      string `json:"url"`
}

// ExtractorResponse represents extraction responses from Vidara, Streamtape, or VidFast.
type ExtractorResponse struct {
	Status         string            `json:"status,omitempty"`
	Success        bool              `json:"success,omitempty"`
	Source         string            `json:"source,omitempty"`
	OriginalURL    string            `json:"original_url,omitempty"`
	URL            string            `json:"url,omitempty"`
	ProxyURL       string            `json:"proxy_url,omitempty"`
	DirectURL      string            `json:"direct_url,omitempty"`
	TapecontentURL string            `json:"tapecontent_url,omitempty"`
	StreamURL      string            `json:"stream_url,omitempty"`
	Text           string            `json:"text,omitempty"`
	Title          string            `json:"title,omitempty"`
	Thumbnail      *string           `json:"thumbnail,omitempty"`
	Subtitles      interface{}       `json:"subtitles,omitempty"`
	AudioTracks    []AudioTrack      `json:"audio_tracks,omitempty"`
	StreamType     string            `json:"stream_type,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	Error          string            `json:"error,omitempty"`
	Message        string            `json:"message,omitempty"`
	ID             string            `json:"id,omitempty"`
}

// ResolveResponse represents dual resolver endpoint response (preserving app.py /api/resolve).
type ResolveResponse struct {
	Status      string             `json:"status"`
	Mode        string             `json:"mode"`
	STID        string             `json:"st_id"`
	VAID        string             `json:"va_id"`
	Primary     *ExtractorResponse `json:"primary,omitempty"`
	Secondary   *ExtractorResponse `json:"secondary,omitempty"`
	ST          *ExtractorResponse `json:"st,omitempty"`
	VA          *ExtractorResponse `json:"va,omitempty"`
	Thumbnail   *string            `json:"thumbnail,omitempty"`
	Subtitles   interface{}        `json:"subtitles,omitempty"`
	AudioTracks []AudioTrack       `json:"audio_tracks,omitempty"`
}
