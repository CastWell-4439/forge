package core

import (
	"encoding/json"
	"time"
)

// QualityLevel defines the quality tier for video production.
type QualityLevel string

const (
	QualityDraft    QualityLevel = "draft"
	QualityStandard QualityLevel = "standard"
	QualityPremium  QualityLevel = "premium"
)

// VideoRequirement represents a structured video production requirement
// parsed from natural language input. Copied from agent-tech-spec 3.2.
type VideoRequirement struct {
	// Basic info
	Description string        `json:"description"`
	Duration    time.Duration `json:"-"`
	DurationSec float64      `json:"duration"` // for JSON marshaling
	AspectRatio string        `json:"aspect_ratio"`
	Resolution  string        `json:"resolution"`

	// Content elements
	FaceSwap  *FaceSwapReq `json:"face_swap,omitempty"`
	LipSync   *LipSyncReq  `json:"lip_sync,omitempty"`
	TTS       *TTSReq      `json:"tts,omitempty"`
	BGM       *BGMReq      `json:"bgm,omitempty"`
	Subtitles *SubtitleReq `json:"subtitles,omitempty"`
	Script    *ScriptReq   `json:"script,omitempty"`

	// Source materials
	SourceVideos []MediaRef `json:"source_videos,omitempty"`
	SourceImages []MediaRef `json:"source_images,omitempty"`
	SourceAudios []MediaRef `json:"source_audios,omitempty"`

	// Quality requirements
	QualityLevel QualityLevel `json:"quality_level"`
}

// FaceSwapReq describes a face swap requirement.
type FaceSwapReq struct {
	TargetFace MediaRef `json:"target_face"`
	AllFaces   bool     `json:"all_faces"`
	FaceIndex  []int    `json:"face_index,omitempty"`
}

// LipSyncReq describes a lip sync requirement.
type LipSyncReq struct {
	Mode string `json:"mode"` // "v1" | "v2"
}

// TTSReq describes a text-to-speech requirement.
type TTSReq struct {
	Text     string  `json:"text"`
	Voice    string  `json:"voice"`
	Language string  `json:"language"`
	Speed    float64 `json:"speed"`
}

// BGMReq describes a background music requirement.
type BGMReq struct {
	Style  string   `json:"style"`
	Source MediaRef `json:"source,omitempty"`
	Volume float64  `json:"volume"`
}

// SubtitleReq describes a subtitle requirement.
type SubtitleReq struct {
	Language string `json:"language"`
	Style    string `json:"style"`
	Position string `json:"position"`
}

// ScriptReq describes a script generation requirement.
type ScriptReq struct {
	Topic    string `json:"topic"`
	Style    string `json:"style"`
	Language string `json:"language"`
}

// MediaRef is a reference to a media file.
type MediaRef struct {
	URL      string `json:"url"`
	Type     string `json:"type"`     // "image" | "video" | "audio"
	Filename string `json:"filename"`
}

// ToPromptString formats the requirement as a human-readable string for LLM prompts.
func (r *VideoRequirement) ToPromptString() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return r.Description
	}
	return string(data)
}
