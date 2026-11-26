package main

import (
	"strings"
	"testing"
	"time"
)

func TestEveryMessageHasBothLanguages(t *testing.T) {
	for key, m := range messages {
		en, fr := m[LangEN], m[LangFR]
		if en == "" || fr == "" {
			t.Errorf("%s: missing translation (en %q, fr %q)", key, en, fr)
		}
		if strings.Count(en, "%s") != strings.Count(fr, "%s") {
			t.Errorf("%s: placeholders differ: %q vs %q", key, en, fr)
		}
	}
}

func TestT(t *testing.T) {
	if got := T(LangFR, "humidity", "74"); got != "Humidité 74 %" {
		t.Errorf("%q", got)
	}
	if got := T("de", "tile_on"); got != "ON" {
		t.Errorf("unknown language should fall back to English: %q", got)
	}
	if got := T(LangEN, "no_such_key"); got != "no_such_key" {
		t.Errorf("%q", got)
	}
}

func TestFormats(t *testing.T) {
	tm := time.Date(2026, 8, 2, 9, 5, 0, 0, time.UTC) // a Sunday
	cases := [][2]string{
		{formatClock(LangEN, tm), "09:05"},
		{formatClock(LangFR, tm), "09 h 05"},
		{formatDate(LangEN, tm), "Sun Aug 2"},
		{formatDate(LangFR, tm), "dim. 2 août"},
		{WindText(LangEN, Wind{"WNW", "22", "38"}), "WNW 22 km/h, gust 38"},
		{WindText(LangFR, Wind{"ONO", "22", "36"}), "ONO 22 km/h, rafales 36"},
		{WindText(LangFR, Wind{"", "calme", ""}), "calme"},
		{WindText(LangEN, Wind{"N", "0", ""}), "calm"},
		{WindText(LangEN, Wind{}), ""},
	}
	for i, c := range cases {
		if c[0] != c[1] {
			t.Errorf("%d: got %q want %q", i, c[0], c[1])
		}
	}
}
